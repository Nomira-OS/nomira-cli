package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRecipe(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mysystem")
	if err := os.MkdirAll(filepath.Join(dir, "validation.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "recipe.yml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSyntaxImage(t *testing.T) {
	cases := map[string]string{
		"# syntax=ghcr.io/x/y:2\nsettings: {}\n":       "ghcr.io/x/y:2",
		"\n#  Syntax = ghcr.io/x/y:2 \nsettings: {}\n": "ghcr.io/x/y:2",
		"# a comment\n# syntax=img:1\nsettings: {}\n":  "img:1",
		"settings: {}\n# syntax=late:1\n":              "",
		"# just a comment\nsettings: {}\n":             "",
	}
	for content, want := range cases {
		if got := syntaxImage(writeRecipe(t, content)); got != want {
			t.Errorf("%q: got %q, want %q", content, got, want)
		}
	}
}

func dryRun(t *testing.T, args ...string) string {
	t.Helper()
	t.Setenv("BUILDKIT_HOST", "")
	noNativeBuildkit(t)
	t.Setenv("NOMIRA_SUDO", "")
	t.Setenv("NOMIRA_BUILDKIT_IMAGE", "")
	var out, errb bytes.Buffer
	if code := runCLI(args, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	return out.String()
}

func TestSwitchDryRunOrder(t *testing.T) {
	asUser(t, false)
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	hook := filepath.Join(filepath.Dir(recipe), "validation.d", "10-lint.sh")
	if err := os.WriteFile(hook, []byte("true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := dryRun(t, "switch", "--dry-run", "-f", recipe, "--build-arg", "SNAPSHOT=2026/11/10", "--apply")

	// Steps must appear in this order: nothing reaches root's storage or
	// bootc before the hooks and the boot test.
	steps := []string{
		"podman run -d --name nomira-buildkitd --privileged -v ",
		":/run/buildkit -v nomira-buildkitd-cache:/var/lib/buildkit docker.io/moby/buildkit:",
		"buildctl --addr unix://",
		"/nomira/buildkit/buildkitd.sock build",
		"--output type=oci,tar=false,dest=",
		".nomira/mysystem.oci,compression=zstd",
		"+ skopeo copy oci:",
		"IMG=localhost/mysystem:latest sh ",
		"+ bcvk ephemeral test-basic --karg systemd.firstboot=off localhost/mysystem:latest",
		"+ sudo bootc switch --transport oci --apply /",
	}
	pos := 0
	for _, s := range steps {
		i := strings.Index(out[pos:], s)
		if i < 0 {
			t.Fatalf("step %q missing or out of order in:\n%s", s, out)
		}
		pos += i + len(s)
	}
	for _, want := range []string{"source=ghcr.io/x/front:9", "build-arg:SNAPSHOT=2026/11/10", "filename=recipe.yml", ".nomira/mysystem.oci,"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestBuildUsesBuildkitHostAndDefaults(t *testing.T) {
	recipe := writeRecipe(t, "settings: {}\n")
	t.Setenv("BUILDKIT_HOST", "unix:///run/buildkit/buildkitd.sock")
	var out, errb bytes.Buffer
	if code := runCLI([]string{"build", "--dry-run", "-f", recipe, "--name", "foo"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if strings.Contains(s, "nomira-buildkitd") {
		t.Errorf("managed container used despite BUILDKIT_HOST:\n%s", s)
	}
	for _, want := range []string{"--addr unix:///run/buildkit/buildkitd.sock", "source=" + defaultFrontend, "foo.oci,"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

// asUser makes nomira believe it runs as root or not, whoever runs the tests.
func asUser(t *testing.T, root bool) {
	t.Helper()
	old := isRoot
	isRoot = func() bool { return root }
	t.Cleanup(func() { isRoot = old })
}

func TestSwitchAsRoot(t *testing.T) {
	recipe := writeRecipe(t, "settings: {}\n")
	asUser(t, true)
	out := dryRun(t, "switch", "--dry-run", "-f", recipe)
	if strings.Contains(out, "sudo") {
		t.Errorf("sudo used although running as root:\n%s", out)
	}
	if n := strings.Count(out, "+ skopeo copy"); n != 1 {
		t.Errorf("want exactly one image load (for the boot test), got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "+ bootc switch --transport oci /") || !strings.Contains(out, "mysystem.oci\n") {
		t.Errorf("bootc switch missing:\n%s", out)
	}
}

// noNativeBuildkit hides any buildkitd running on the test machine.
func noNativeBuildkit(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	old := systemSocket
	systemSocket = filepath.Join(t.TempDir(), "absent.sock")
	t.Cleanup(func() { systemSocket = old })
}

func TestBuildPrefersNativeSocket(t *testing.T) {
	recipe := writeRecipe(t, "settings: {}\n")
	t.Setenv("BUILDKIT_HOST", "")
	noNativeBuildkit(t)
	// A rootless buildkitd in the user's runtime directory.
	run := os.Getenv("XDG_RUNTIME_DIR")
	if err := os.MkdirAll(filepath.Join(run, "buildkit"), 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(run, "buildkit", "buildkitd.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot create unix socket: %v", err)
	}
	defer l.Close()

	var out, errb bytes.Buffer
	if code := runCLI([]string{"build", "--dry-run", "-f", recipe}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "--addr unix://"+sock) || strings.Contains(s, "nomira-buildkitd") {
		t.Errorf("native socket not used:\n%s", s)
	}
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCLI([]string{"version"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "alpha") {
		t.Errorf("exit %d, output %q", code, out.String())
	}
}

func TestErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCLI([]string{"frobnicate"}, &out, &errb); code != 2 {
		t.Errorf("unknown command: exit %d", code)
	}
	if code := runCLI([]string{"build", "-f", "/nonexistent/recipe.yml"}, &out, &errb); code != 1 {
		t.Errorf("missing recipe: exit %d", code)
	}
	recipe := writeRecipe(t, "settings: {}\n")
	if code := runCLI([]string{"build", "--dry-run", "-f", recipe, "--build-arg", "NOVALUE"}, &out, &errb); code != 1 {
		t.Errorf("bad build-arg: exit %d", code)
	}
}

func TestRunnerTimes(t *testing.T) {
	var buf bytes.Buffer
	r := &runner{out: &buf, start: time.Now()}
	if err := r.run("true"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "[0:00] + true") || !strings.Contains(out, "[0:00] true took 0:00") {
		t.Errorf("output: %q", out)
	}
	if got := clock(83 * time.Second); got != "1:23" {
		t.Errorf("clock: %s", got)
	}
}

func TestSwitchWithoutChecksCopiesNothing(t *testing.T) {
	recipe := writeRecipe(t, "settings: {}\n")
	asUser(t, true)
	out := dryRun(t, "switch", "--dry-run", "--no-boot-test", "-f", recipe)
	if strings.Contains(out, "skopeo") {
		t.Errorf("image copied although nothing needs it:\n%s", out)
	}
	if !strings.Contains(out, "+ bootc switch --transport oci /") {
		t.Errorf("bootc switch missing:\n%s", out)
	}
}

func TestSystemRecipeByDefault(t *testing.T) {
	t.Setenv("NOMIRA_RECIPE", "")
	old := systemOutput
	systemOutput = filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { systemOutput = old })
	out := dryRun(t, "build", "--dry-run")
	for _, want := range []string{"--local dockerfile=/etc/nomira", "filename=recipe.yml", "dest=" + systemOutput + "/nomira.oci,"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	recipe := writeRecipe(t, "settings: {}\n")
	t.Setenv("NOMIRA_RECIPE", recipe)
	out = dryRun(t, "build", "--dry-run")
	if !strings.Contains(out, "--local dockerfile="+filepath.Dir(recipe)) || !strings.Contains(out, filepath.Join(filepath.Dir(recipe), ".nomira", "mysystem.oci")) {
		t.Errorf("NOMIRA_RECIPE not used:\n%s", out)
	}
}
