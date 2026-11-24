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

// options are the flags shared by the subcommands.
type options struct {
	recipe     string
	context    string
	frontend   string
	name       string
	output     string
	buildArgs  multiFlag
	secrets    multiFlag
	dryRun     bool
	noBootTest bool
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func (o *options) register(fs *flag.FlagSet, cmd string) {
	fs.StringVar(&o.recipe, "f", defaultRecipe(), "recipe entry file (default: $NOMIRA_RECIPE, else "+systemRecipe+")")
	fs.StringVar(&o.frontend, "frontend", "", "frontend image (default: the recipe's `# syntax=` line, else "+defaultFrontend+")")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the commands instead of running them")
	if cmd == "build" {
		fs.StringVar(&o.name, "name", "", "image name (default: settings.name, else the directory name)")
		fs.StringVar(&o.output, "o", "", "OCI layout directory (default: "+systemOutput+"/<name>.oci for a recipe in /etc, else .nomira/<name>.oci next to the recipe)")
	}
	if cmd == "build" {
		fs.StringVar(&o.context, "context", "", "build context directory (default: the recipe's directory)")
		fs.Var(&o.buildArgs, "build-arg", "override a declared arg, NAME=VALUE (repeatable)")
		fs.Var(&o.secrets, "secret", "pass a secret to the build, id=ID,src=PATH (repeatable; see buildctl --secret)")
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
	if o.frontend == "" {
		o.frontend = syntaxImage(o.recipe)
	}
	if o.frontend == "" {
		o.frontend = defaultFrontend
	}
	return nil
}

func (o *options) recipeDir() string {
	if d := filepath.Dir(o.recipe); d != "" {
		return d
	}
	return "."
}

func (o *options) imageRef(r *runner) string {
	if o.name == "" {
		o.name = imageName(r, o.frontend, o.recipe)
	}
	return "localhost/" + o.name + ":latest"
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
	o.imageRef(r) // settles o.name
	if o.output == "" {
		o.output = filepath.Join(o.outputDir(), o.name+".oci")
	}
	work := workDir(o.output)
	if o.context == "" {
		o.context = o.recipeDir()
	}
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
		"--local", "context=" + o.context,
		"--local", "dockerfile=" + o.recipeDir(),
		"--opt", "filename=" + filepath.Base(o.recipe),
	}
	for _, a := range o.buildArgs {
		if !strings.Contains(a, "=") {
			return "", fmt.Errorf("--build-arg %q: expected NAME=VALUE", a)
		}
		args = append(args, "--opt", "build-arg:"+a)
	}
	// zstd: about four times faster than gzip at the same size. bootc reads
	// zstd layers; layers taken over from the base image stay as they are.
	for _, sec := range o.secrets {
		args = append(args, "--secret", sec)
	}
	args = append(args, "--output", "type=oci,tar=false,dest="+work+",compression=zstd")
	if err := r.run("buildctl", args...); err != nil {
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
			fmt.Fprintln(os.Stderr, "nomira: bcvk not found, skipping the boot test (use --no-boot-test to silence)")
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
