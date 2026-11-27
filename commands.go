package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// options are the flags of the subcommands; nomira.conf can set them too.
//
// What a frontend can act on is in the recipe; what only the BuildKit
// client can set (layer compression, the files behind secrets, the pull
// policy, where the result goes) is a flag. That is the rule: what has to
// be a client option does not belong in the recipe.
type options struct {
	recipe      string
	frontend    string // from the recipe's # syntax= line
	info        *recipeInfo
	output      string
	progress    string
	dryRun      bool
	noBootTest  bool
	logFormat   string
	color       string
	origin      bool
	config      string
	name        string
	compression string
	secrets     secrets
	pull        string
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
	fs.StringVar(&o.pull, "pull", "missing", "images: always pull them again, pull missing ones, or never pull (podman --pull; BuildKit image-resolve-mode)")
	if cmd == "recipe export" {
		fs.StringVar(&o.output, "o", "", "write the recipe to this file instead of stdout")
	}
	if cmd == "recipe diff" {
		fs.BoolVar(&o.origin, "origin", false, "also compare where each part came from (the # from comments)")
	}
	if cmd == "build" {
		fs.StringVar(&o.output, "o", "", "OCI layout directory (default: "+systemOutput+"/<name>.oci for a recipe in /etc, else .nomira/<name>.oci next to the recipe)")
		fs.StringVar(&o.name, "name", "", "image name for the output directory and local copies (default: settings.name of the recipe, else the recipe directory's name)")
		fs.StringVar(&o.compression, "compression", "zstd", "layer compression: zstd, gzip or uncompressed")
		fs.Var(&o.secrets, "secret", "id=ID,src=FILE: a secret the recipe mounts; may be repeated")
		fs.StringVar(&o.progress, "progress", viewNomira, "progress in text mode: nomira (one line per step), or buildctl's own display: plain, tty, auto")
	}
	if cmd == "validate" || cmd == "build" {
		fs.BoolVar(&o.noBootTest, "no-boot-test", false, "skip the bcvk boot test")
	}
}

// The machine's own recipe lives in /etc/nomira: /etc belongs to the
// machine and keeps local changes across updates. Its build output goes to
// /var/lib/nomira, since images do not belong in /etc.
const systemRecipe = "/etc/nomira/recipe.yml"

var systemOutput = "/var/lib/nomira"

func defaultRecipe() string {
	if f := os.Getenv("NOMIRA_RECIPE"); f != "" {
		return f
	}
	return systemRecipe
}

// outputDir is where the OCI directory goes unless -o says otherwise.
func (o *options) outputDir() string {
	dir, err := filepath.Abs(o.recipeDir())
	if err == nil && (dir == "/etc" || strings.HasPrefix(dir, "/etc/")) {
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

// imageName is --name, else the name the recipe gives itself.
func (o *options) imageName(r *runner) string {
	if o.name != "" {
		return o.name
	}
	return o.recipeInfo(r).Name
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

// recipeInfo asks the frontend once for the settings the CLI acts on.
func (o *options) recipeInfo(r *runner) *recipeInfo {
	if o.info == nil {
		o.info = inspectRecipe(r, o.frontend, o.recipe)
	}
	return o.info
}

func (o *options) imageRef(r *runner) string {
	return "localhost/" + o.imageName(r) + ":latest"
}

// frontendMode runs lint or graph through the frontend image.
func frontendMode(r *runner, o *options, mode string) error {
	if err := r.need("podman"); err != nil {
		return err
	}
	return r.run("podman", frontendArgs(o.frontend, o.recipe, mode)...)
}

// build solves the recipe with BuildKit and writes an OCI layout
// directory. It returns the directory.
//
// A directory rather than an archive: BuildKit only writes the blobs that
// are not there yet, and bootc reads the directory directly, so an
// unchanged layer is neither exported nor copied again.
func build(r *runner, o *options) (string, error) {
	if err := r.need("buildctl", "podman"); err != nil {
		return "", err
	}
	info := o.recipeInfo(r)
	switch o.compression {
	case "zstd", "gzip", "uncompressed":
	default:
		return "", fmt.Errorf("--compression: %q is not zstd, gzip or uncompressed", o.compression)
	}
	if o.output == "" {
		o.output = filepath.Join(o.outputDir(), o.imageName(r)+".oci")
	}
	work := workDir(o.output)
	if !r.dryRun {
		if err := os.MkdirAll(filepath.Dir(work), 0o755); err != nil {
			return "", err
		}
	}
	addr, err := buildkitAddr(r)
	if err != nil {
		return "", err
	}
	args := []string{
		"--addr", addr, "build",
		"--frontend", "gateway.v0",
		"--opt", "source=" + o.frontend,
		"--local", "context=" + o.recipeDir(),
		"--local", "dockerfile=" + o.recipeDir(),
		"--opt", "filename=" + filepath.Base(o.recipe),
	}
	if mode := resolveMode(o.pull); mode != "" {
		args = append(args, "--opt", "image-resolve-mode="+mode)
	}
	for _, s := range o.secrets {
		args = append(args, "--secret", s)
	}
	// A secret the recipe mounts without a source: buildctl would only
	// fail at that step, or silently skip an optional one.
	given := o.secrets.ids()
	ids := make([]string, 0, len(info.Secrets))
	for id := range info.Secrets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if given[id] {
			continue
		}
		if info.Secrets[id] {
			return "", fmt.Errorf("the recipe requires the secret %q: give it with --secret id=%s,src=FILE or in %s", id, id, confName)
		}
		r.logger().warn(fmt.Sprintf("the recipe mounts the secret %q, which is not given; the step runs without it", id))
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
	// zstd by default, about four times faster than gzip at the same size.
	// rewrite-timestamp clamps the file times in the recipe's layers to the
	// source date epoch the frontend reports (settings.source_date_epoch),
	// so the same recipe gives the same layers; without an epoch BuildKit
	// leaves them as they are. Layers taken over from the base image stay
	// as they are either way.
	output := "type=oci,tar=false,dest=" + work + ",compression=" + o.compression + ",rewrite-timestamp=true"
	args = append(args, "--output", output)
	if err := r.runFramed(view, "buildctl", args...); err != nil {
		return "", err
	}
	if !r.dryRun {
		if err := pruneLayout(work); err != nil {
			return "", fmt.Errorf("prune %s: %w", work, err)
		}
	}
	return work, nil
}

// load copies the image from the layout directory into the user's
// containers-storage, where podman and bcvk find it.
func load(r *runner, dir, ref string) error {
	return r.run("skopeo", "copy", "oci:"+dir, "containers-storage:"+ref)
}

// hooks returns the recipe's validation.d scripts in order.
func hooks(o *options) []string {
	h, _ := filepath.Glob(filepath.Join(o.recipeDir(), "validation.d", "*.sh"))
	sort.Strings(h)
	return h
}

// bootTest reports whether the bcvk boot test will run.
func bootTest(r *runner, o *options) bool {
	if o.noBootTest {
		return false
	}
	if r.dryRun {
		return true
	}
	_, err := exec.LookPath("bcvk")
	return err == nil
}

// validate runs the recipe's validation.d hooks against the image (which
// must be in the user's containers-storage), then the bcvk boot test.
func validate(r *runner, o *options, ref string) error {
	for _, h := range hooks(o) {
		if err := r.runEnv([]string{"IMG=" + ref}, "sh", h); err != nil {
			return fmt.Errorf("validation hook %s failed: %w", h, err)
		}
	}
	if o.noBootTest {
		return nil
	}
	if !r.dryRun {
		if _, err := exec.LookPath("bcvk"); err != nil {
			r.logger().warn("bcvk not found, skipping the boot test (use --no-boot-test to silence)")
			return nil
		}
	}
	// The base image asks for locale, root password etc. on its first boot;
	// an unattended test VM must not wait for answers.
	if err := r.run("bcvk", "ephemeral", "test-basic", "--karg", "systemd.firstboot=off", ref); err != nil {
		return fmt.Errorf("boot test failed: %w", err)
	}
	return nil
}

// workDir is where an image is built and checked before it is published
// to dir.
func workDir(dir string) string {
	return filepath.Join(filepath.Dir(dir), ".build", filepath.Base(dir))
}

// buildAndCheck builds the recipe into the work directory, runs the checks
// and only then publishes the image to the output directory. The output
// directory therefore only ever holds images that passed, which is what
// makes it safe for bootc to follow it: bootc upgrade takes every new
// image from there.
func buildAndCheck(r *runner, o *options) (string, error) {
	work, err := build(r, o)
	if err != nil {
		return "", err
	}
	// Hooks and the boot test run the image with podman and bcvk, which
	// need it in containers-storage. Without them nothing is copied.
	if len(hooks(o)) > 0 || bootTest(r, o) {
		ref := o.imageRef(r)
		if err := load(r, work, ref); err != nil {
			return "", err
		}
		if err := validate(r, o, ref); err != nil {
			return "", err
		}
	}
	if r.dryRun {
		r.echo(nil, "nomira-publish", []string{work, o.output})
		return o.output, nil
	}
	return o.output, publish(work, o.output)
}

// isRoot reports whether nomira runs as root. A variable so tests can set it.
var isRoot = func() bool { return os.Geteuid() == 0 }
