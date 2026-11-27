package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// options are the flags of the subcommands; nomira.conf can set them too.
//
// What a frontend can act on is in the recipe; what only the BuildKit
// client can set (image name, layer compression, the files behind
// secrets, the pull policy, where the result goes) is a flag. That is the
// rule: what has to be a client option does not belong in the recipe.
type options struct {
	recipe      string
	frontend    string // from the recipe's # syntax= line
	output      string
	progress    string
	dryRun      bool
	logFormat   string
	color       string
	origin      bool
	config      string
	name        string
	compression string
	secrets     secrets
	pull        string
	noCheck     bool
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func (o *options) register(fs *flag.FlagSet, cmd string) {
	fs.StringVar(&o.recipe, "f", defaultRecipe(), "recipe entry file (default: $NOMIRA_RECIPE, else "+systemRecipe+")")
	fs.StringVar(&o.logFormat, "log-format", envOr("NOMIRA_LOG_FORMAT", formatText), "output: text for people, json for log collectors (one object per line)")
	fs.StringVar(&o.color, "color", envOr("NOMIRA_COLOR", "auto"), "colour in text output: auto (on a terminal, unless NO_COLOR is set), always or never")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the commands instead of running them")
	fs.StringVar(&o.config, "config", "", "nomira.conf to read (default: $NOMIRA_CONFIG, else nomira.conf next to the recipe)")
	fs.StringVar(&o.pull, "pull", "missing", "images: always pull them again, pull missing ones, or never pull (BuildKit's image-resolve-mode)")
	fs.StringVar(&o.progress, "progress", viewNomira, "progress in text mode: nomira (one line per step), or buildctl's own display: plain, tty, auto")
	if cmd == "recipe export" {
		fs.StringVar(&o.output, "o", "", "write the recipe to this file instead of stdout")
	}
	if cmd == "recipe diff" {
		fs.BoolVar(&o.origin, "origin", false, "also compare where each part came from (the # from comments)")
	}
	if cmd == "build" {
		fs.StringVar(&o.output, "o", "", "OCI layout directory (default: "+systemOutput+"/<name>.oci for a recipe in /etc, else .nomira/<name>.oci next to the recipe)")
		fs.StringVar(&o.name, "name", "", "image name, for the output directory (default: "+systemName+" for a recipe in /etc, else the recipe directory's name)")
		fs.StringVar(&o.compression, "compression", "zstd", "layer compression: zstd, gzip or uncompressed")
		fs.Var(&o.secrets, "secret", "id=ID,src=FILE: a secret the recipe mounts; may be repeated")
		fs.BoolVar(&o.noCheck, "no-check", false, "skip the conflict checks between groups and between buckets: faster, but overlapping groups are merged in a fixed order without a word")
	}
}

// The machine's own recipe lives in /etc/nomira: /etc belongs to the
// machine and keeps local changes across updates. Its build output goes to
// /var/lib/nomira, since images do not belong in /etc, and is called
// "system".
const (
	systemRecipe = "/etc/nomira/recipe.yml"
	systemName   = "system"
)

var systemOutput = "/var/lib/nomira"

func defaultRecipe() string {
	if f := os.Getenv("NOMIRA_RECIPE"); f != "" {
		return f
	}
	return systemRecipe
}

// inEtc reports whether the recipe is the machine's own, below /etc.
func (o *options) inEtc() bool {
	dir, err := filepath.Abs(o.recipeDir())
	return err == nil && (dir == "/etc" || strings.HasPrefix(dir, "/etc/"))
}

// outputDir is where the OCI directory goes unless -o says otherwise.
func (o *options) outputDir() string {
	if o.inEtc() {
		return systemOutput
	}
	return filepath.Join(o.recipeDir(), ".nomira")
}

// resolve fills in defaults that depend on the recipe.
func (o *options) resolve(r *runner) error {
	if !r.dryRun {
		if _, err := os.Stat(o.recipe); err != nil {
			return fmt.Errorf("recipe %s not found (use -f or NOMIRA_RECIPE)", o.recipe)
		}
	}
	o.frontend = syntaxImage(o.recipe)
	if _, err := os.Stat(o.recipe); err != nil && r.dryRun {
		o.frontend = "<frontend from the # syntax= line>"
	}
	if o.frontend == "" {
		return fmt.Errorf("%s has no first line \"# syntax=<frontend image>\"; the recipe names its frontend", o.recipe)
	}
	return nil
}

func (o *options) recipeDir() string {
	if d := filepath.Dir(o.recipe); d != "" {
		return d
	}
	return "."
}

// imageName is --name, else "system" for the machine's own recipe, else
// the name of the recipe's directory.
func (o *options) imageName() string {
	switch {
	case o.name != "":
		return o.name
	case o.inEtc():
		return systemName
	}
	if abs, err := filepath.Abs(o.recipeDir()); err == nil {
		return strings.ToLower(filepath.Base(abs))
	}
	return "nomira"
}

// resolveMode maps --pull to BuildKit's image-resolve-mode; "" for its
// default.
func resolveMode(pull string) string {
	switch pull {
	case "always":
		return "pull"
	case "never":
		return "local"
	}
	return ""
}

// solve runs buildctl on the recipe: the frontend from its # syntax=
// line, the recipe directory as context, the frontend option mode
// ("" for a build) and the given output.
func solve(r *runner, o *options, mode, output string) error {
	if err := r.need("buildctl"); err != nil {
		return err
	}
	addr, err := buildkitAddr(r)
	if err != nil {
		return err
	}
	args := []string{
		"--addr", addr, "build",
		"--frontend", "gateway.v0",
		"--opt", "source=" + o.frontend,
		"--local", "context=" + o.recipeDir(),
		"--local", "dockerfile=" + o.recipeDir(),
		"--opt", "filename=" + filepath.Base(o.recipe),
	}
	if mode != "" {
		args = append(args, "--opt", "mode="+mode)
	}
	if o.noCheck && mode == "" {
		args = append(args, "--opt", "check=off")
	}
	if m := resolveMode(o.pull); m != "" {
		args = append(args, "--opt", "image-resolve-mode="+m)
	}
	// Where a secret the recipe mounts comes from. One that a step
	// requires but nobody gives fails that step; BuildKit says which.
	for _, s := range o.secrets {
		args = append(args, "--secret", s)
	}
	// buildctl reports in rawjson, which nomira shows in its own way (see
	// log.go), unless --progress asks for buildctl's own display.
	view := viewNomira
	switch {
	case r.logger().format == formatJSON:
		view = viewEvents
		args = append(args, "--progress", "rawjson")
	case o.progress == "" || o.progress == viewNomira:
		args = append(args, "--progress", "rawjson")
	default:
		view = viewRaw
		args = append(args, "--progress", o.progress)
	}
	args = append(args, "--output", output)
	return r.runFramed(view, "buildctl", args...)
}

// ask runs the frontend in mode lint or resolve and returns the
// directory with its answer (version, and recipe.yml for resolve), to be
// removed by the caller. The frontend's version is checked against the
// CLI's.
func ask(r *runner, o *options, mode string) (string, error) {
	dir := filepath.Join(os.TempDir(), "nomira-"+mode)
	if !r.dryRun {
		var err error
		if dir, err = os.MkdirTemp("", "nomira-"+mode+"-"); err != nil {
			return "", err
		}
	}
	if err := solve(r, o, mode, "type=local,dest="+dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if !r.dryRun {
		v, _ := os.ReadFile(filepath.Join(dir, "version"))
		checkVersion(r, strings.TrimSpace(string(v)))
	}
	return dir, nil
}

// lint asks the frontend whether the recipe and its imports parse.
func lint(r *runner, o *options) error {
	dir, err := ask(r, o, "lint")
	if err == nil && !r.dryRun {
		os.RemoveAll(dir)
		r.logger().info(o.recipe + " is fine")
	}
	return err
}

// checkVersion warns when the frontend that answered is not one the CLI
// is known to work with.
func checkVersion(r *runner, frontend string) {
	if w := compatibility(cliVersion(), frontend); w != "" {
		r.logger().warn(w)
	}
}

// build solves the recipe with BuildKit and writes an OCI layout
// directory. It returns the directory.
//
// A directory rather than an archive: BuildKit only writes the blobs that
// are not there yet, and bootc reads the directory directly, so an
// unchanged layer is neither exported nor copied again.
func build(r *runner, o *options) (string, error) {
	switch o.compression {
	case "zstd", "gzip", "uncompressed":
	default:
		return "", fmt.Errorf("--compression: %q is not zstd, gzip or uncompressed", o.compression)
	}
	if o.output == "" {
		o.output = filepath.Join(o.outputDir(), o.imageName()+".oci")
	}
	work := workDir(o.output)
	if !r.dryRun {
		if err := os.MkdirAll(filepath.Dir(work), 0o755); err != nil {
			return "", err
		}
	}
	// zstd by default, about four times faster than gzip at the same size.
	// rewrite-timestamp clamps the file times in the recipe's layers to the
	// source date epoch the frontend reports (settings.source_date_epoch),
	// so the same recipe gives the same layers; without an epoch BuildKit
	// leaves them as they are. Layers taken over from the base image stay
	// as they are either way.
	output := "type=oci,tar=false,dest=" + work + ",compression=" + o.compression + ",rewrite-timestamp=true"
	if err := solve(r, o, "", output); err != nil {
		return "", err
	}
	if r.dryRun {
		return work, nil
	}
	if err := pruneLayout(work); err != nil {
		return "", fmt.Errorf("prune %s: %w", work, err)
	}
	labels, err := layoutLabels(work)
	if err != nil {
		return "", fmt.Errorf("read the image config in %s: %w", work, err)
	}
	checkVersion(r, labels[frontendVersionLabel])
	return work, nil
}

// workDir is where an image is built before it is published to dir.
func workDir(dir string) string {
	return filepath.Join(filepath.Dir(dir), ".build", filepath.Base(dir))
}

// buildAndPublish builds the recipe into the work directory and only then
// publishes the image to the output directory. The output directory
// therefore only ever holds complete images that the recipe's own checks
// (bootc container lint, say, as a step of the recipe) passed, which is
// what makes it safe for bootc to follow it: bootc upgrade takes every new
// image from there.
func buildAndPublish(r *runner, o *options) (string, error) {
	work, err := build(r, o)
	if err != nil {
		return "", err
	}
	if r.dryRun {
		r.echo(nil, "nomira-publish", []string{work, o.output})
		return o.output, nil
	}
	return o.output, publish(work, o.output)
}

// isRoot reports whether nomira runs as root. A variable so tests can set it.
var isRoot = func() bool { return os.Geteuid() == 0 }
