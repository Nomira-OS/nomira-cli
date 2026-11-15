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
	dryRun     bool
	noBootTest bool
	apply      bool
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func (o *options) register(fs *flag.FlagSet, cmd string) {
	fs.StringVar(&o.recipe, "f", "recipe.yml", "recipe entry file")
	fs.StringVar(&o.frontend, "frontend", "", "frontend image (default: the recipe's `# syntax=` line, else "+defaultFrontend+")")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the commands instead of running them")
	if cmd == "build" || cmd == "switch" {
		fs.StringVar(&o.context, "context", "", "build context directory (default: the recipe's directory)")
		fs.StringVar(&o.name, "name", "", "image name (default: settings.name, else the directory name)")
		fs.StringVar(&o.output, "o", "", "OCI archive to write (default: .nomira/<name>.oci.tar next to the recipe)")
		fs.Var(&o.buildArgs, "build-arg", "override a declared arg, NAME=VALUE (repeatable)")
	}
	if cmd == "validate" || cmd == "switch" {
		fs.BoolVar(&o.noBootTest, "no-boot-test", false, "skip the bcvk boot test")
	}
	if cmd == "switch" {
		fs.BoolVar(&o.apply, "apply", false, "reboot into the new image right away (bootc switch --apply)")
	}
}

// resolve fills in defaults that depend on the recipe.
func (o *options) resolve(r *runner) error {
	if !r.dryRun {
		if _, err := os.Stat(o.recipe); err != nil {
			return fmt.Errorf("recipe %s not found (use -f)", o.recipe)
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

// build solves the recipe with BuildKit and writes an OCI archive.
// It returns the archive path.
func build(r *runner, o *options) (string, error) {
	if err := r.need("buildctl", "podman"); err != nil {
		return "", err
	}
	o.imageRef(r) // settles o.name
	if o.output == "" {
		o.output = filepath.Join(o.recipeDir(), ".nomira", o.name+".oci.tar")
	}
	if o.context == "" {
		o.context = o.recipeDir()
	}
	if !r.dryRun {
		if err := os.MkdirAll(filepath.Dir(o.output), 0o755); err != nil {
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
	args = append(args, "--output", "type=oci,dest="+o.output)
	return o.output, r.run("buildctl", args...)
}

// load copies the archive into a containers-storage, optionally as root.
func load(r *runner, archive, ref string, asRoot bool) error {
	args := []string{"copy", "oci-archive:" + archive, "containers-storage:" + ref}
	if asRoot {
		return r.run(sudo(), append([]string{"skopeo"}, args...)...)
	}
	return r.run("skopeo", args...)
}

// validate runs the recipe's validation.d hooks against the image (which
// must be in the user's containers-storage), then the bcvk boot test.
func validate(r *runner, o *options, ref string) error {
	hooks, _ := filepath.Glob(filepath.Join(o.recipeDir(), "validation.d", "*.sh"))
	sort.Strings(hooks)
	for _, h := range hooks {
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
	if err := r.run("bcvk", "ephemeral", "test-basic", ref); err != nil {
		return fmt.Errorf("boot test failed: %w", err)
	}
	return nil
}

// switchTo is the full workflow: build, validate, then hand the image to
// bootc. Nothing touches the running system before validation has passed.
func switchTo(r *runner, o *options) error {
	if err := r.need("buildctl", "podman", "skopeo"); err != nil {
		return err
	}
	archive, err := build(r, o)
	if err != nil {
		return err
	}
	ref := o.imageRef(r)
	if err := load(r, archive, ref, false); err != nil {
		return err
	}
	if err := validate(r, o, ref); err != nil {
		return err
	}
	// bootc runs as root and reads root's containers-storage.
	if err := load(r, archive, ref, true); err != nil {
		return err
	}
	args := []string{"bootc", "switch", "--transport", "containers-storage"}
	if o.apply {
		args = append(args, "--apply")
	}
	return r.run(sudo(), append(args, ref)...)
}

func sudo() string {
	if s := os.Getenv("NOMIRA_SUDO"); s != "" {
		return s
	}
	return "sudo"
}
