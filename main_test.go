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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	t.Setenv("NOMIRA_BUILDKIT_IMAGE", "")
	var out, errb bytes.Buffer
	if code := runCLI(args, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	return out.String()
}

func TestBuildDryRunOrder(t *testing.T) {
	asUser(t, false)
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	out := dryRun(t, "build", "--dry-run", "-f", recipe)

	// Steps must appear in this order: start buildkitd, build, publish.
	// Building never calls bootc, podman only for a managed buildkitd.
	steps := []string{
		"podman run -d --name nomira-buildkitd --privileged -v ",
		":/run/buildkit -v nomira-buildkitd-cache:/var/lib/buildkit docker.io/moby/buildkit:",
		"buildctl --addr unix://",
		"/nomira/buildkit/buildkitd.sock build",
		"--output type=oci,tar=false,dest=",
		".nomira/.build/mysystem.oci,compression=zstd,rewrite-timestamp=true",
		"+ nomira-publish ",
	}
	pos := 0
	for _, s := range steps {
		i := strings.Index(out[pos:], s)
		if i < 0 {
			t.Fatalf("step %q missing or out of order in:\n%s", s, out)
		}
		pos += i + len(s)
	}
	for _, want := range []string{"source=ghcr.io/x/front:9", "filename=recipe.yml"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"bootc", "skopeo", "bcvk", "mode=", "podman run --rm"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("build calls %s:\n%s", unwanted, out)
		}
	}
}

func TestBuildUsesBuildkitHostAndDefaults(t *testing.T) {
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	t.Setenv("BUILDKIT_HOST", "unix:///run/buildkit/buildkitd.sock")
	var out, errb bytes.Buffer
	if code := runCLI([]string{"build", "--dry-run", "-f", recipe}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if strings.Contains(s, "nomira-buildkitd") {
		t.Errorf("managed container used despite BUILDKIT_HOST:\n%s", s)
	}
	for _, want := range []string{"--addr unix:///run/buildkit/buildkitd.sock", "source=ghcr.io/x/front:9", "mysystem.oci,"} {
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

// noNativeBuildkit hides any buildkitd running on the test machine.
func noNativeBuildkit(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	old := systemSocket
	systemSocket = filepath.Join(t.TempDir(), "absent.sock")
	t.Cleanup(func() { systemSocket = old })
}

func TestBuildPrefersNativeSocket(t *testing.T) {
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
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
	if code := runCLI([]string{"version"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "nomira ") || len(out.String()) < 9 {
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
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	// Flags for what the recipe says (frontend, args, context) do not exist.
	for _, flag := range []string{"--build-arg", "--frontend", "--context", "--secret"} {
		if code := runCLI([]string{"build", "--dry-run", "-f", recipe, flag, "x"}, &out, &errb); code != 2 {
			t.Errorf("%s still accepted: exit %d", flag, code)
		}
	}
	noSyntax := writeRecipe(t, "settings: {}\n")
	errb.Reset()
	if code := runCLI([]string{"build", "--dry-run", "-f", noSyntax}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "# syntax=") {
		t.Errorf("recipe without syntax line: exit %d: %s", code, errb.String())
	}
}

func TestRunnerTimes(t *testing.T) {
	var buf bytes.Buffer
	r := &runner{out: &buf, start: time.Now()}
	if err := r.run("true"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, " 0:00  + true\n") || !strings.Contains(out, " 0:00  ✓ true 0:00\n") {
		t.Errorf("output: %q", out)
	}
	if got := clock(83 * time.Second); got != "1:23" {
		t.Errorf("clock: %s", got)
	}
}

func TestJSONLog(t *testing.T) {
	var buf bytes.Buffer
	l, err := newLogger(&buf, formatJSON, "never", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{log: l}
	if err := r.run("true", "x y"); err != nil {
		t.Fatal(err)
	}
	l.buildkitEvents(strings.NewReader(`{"vertexes":[{"digest":"d1","name":"[1/2 packages] run pacman","started":"2026-01-01T00:00:00Z"}]}
{"vertexes":[{"digest":"d1","started":"2026-01-01T00:00:00Z","completed":"2026-01-01T00:00:02Z"}],"logs":[{"vertex":"d1","stream":1,"data":"aW5zdGFsbGluZwpkb25lCg=="}]}
error: failed to solve
`))
	want := []string{
		`{"level":"INFO","msg":"command started","component":"nomira","cmd":"true","args":"'x y'","env":""}`,
		`{"level":"INFO","msg":"command finished","component":"nomira","cmd":"true","duration_ms":`,
		`{"level":"INFO","msg":"step finished","component":"buildkit","step":"[1/2 packages] run pacman","cached":false,"duration_ms":2000}`,
		`{"level":"INFO","msg":"installing","component":"buildkit","step":"[1/2 packages] run pacman","stream":"stdout"}`,
		`{"level":"INFO","msg":"done","component":"buildkit","step":"[1/2 packages] run pacman","stream":"stdout"}`,
		`{"level":"ERROR","msg":"error: failed to solve","component":"buildkit"}`,
	}
	for _, w := range want {
		if !strings.Contains(buf.String(), w) {
			t.Errorf("missing %s in:\n%s", w, buf.String())
		}
	}
	if _, err := newLogger(&buf, "xml", "auto", time.Time{}); err == nil {
		t.Error("--log-format xml accepted")
	}
}

func TestTextCountsFetches(t *testing.T) {
	var buf bytes.Buffer
	l, err := newLogger(&buf, formatText, "never", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	l.buildkitText(strings.NewReader(`{"vertexes":[{"digest":"f1","name":"[1/2 packages/packages] fetch a-1-1-any.pkg.tar.zst","started":"2026-01-01T00:00:00Z","completed":"2026-01-01T00:00:01Z"}]}
{"vertexes":[{"digest":"f2","name":"[1/2 packages/packages] fetch b-1-1-any.pkg.tar.zst","started":"2026-01-01T00:00:00Z","completed":"2026-01-01T00:00:00Z","cached":true}]}
{"vertexes":[{"digest":"f3","name":"[1/2 packages/packages] fetch c-1-1-any.pkg.tar.zst","started":"2026-01-01T00:00:00Z","completed":"2026-01-01T00:00:01Z","error":"404 Not Found"}]}
{"vertexes":[{"digest":"e1","name":"[1/2 packages/packages] each bucket 07: a","started":"2026-01-01T00:00:01Z","completed":"2026-01-01T00:00:02Z"}]}
`))
	out := buf.String()
	for _, want := range []string{"✗ [1/2 packages/packages] fetch c-1-1-any.pkg.tar.zst", "404 Not Found", "✓ [1/2 packages/packages] each bucket 07: a", "files fetched: 1 downloaded, 1 from the cache"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, not := range []string{"fetch a-1-1", "fetch b-1-1", "steps from the cache"} {
		if strings.Contains(out, not) {
			t.Errorf("%q shown in:\n%s", not, out)
		}
	}
	if fetchStep("[1/2 features/ssh] run fetch x") || !fetchStep("[1/2 p/p] fetch x") {
		t.Error("fetchStep")
	}
}

func TestNoCheck(t *testing.T) {
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	if out := dryRun(t, "build", "--dry-run", "--no-check", "-f", recipe); !strings.Contains(out, "--opt check=off") {
		t.Errorf("--no-check:\n%s", out)
	}
	if out := dryRun(t, "build", "--dry-run", "-f", recipe); strings.Contains(out, "check=off") {
		t.Errorf("check off by default:\n%s", out)
	}
}

func TestInlineCache(t *testing.T) {
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	out := dryRun(t, "build", "--dry-run", "--cache-from", "ghcr.io/me/laptop:sha-1", "-f", recipe)
	for _, want := range []string{"--export-cache type=inline", "--import-cache type=registry,ref=ghcr.io/me/laptop:sha-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "type=local,src=") {
		t.Errorf("no published image, but a local import:\n%s", out)
	}
	// The last published image is imported.
	dir := filepath.Join(filepath.Dir(recipe), ".nomira", filepath.Base(filepath.Dir(recipe))+".oci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"manifests":[{"digest":"sha256:abc"}]}`), 0o644)
	out = dryRun(t, "build", "--dry-run", "-f", recipe)
	if !strings.Contains(out, "--import-cache type=local,src="+dir+",digest=sha256:abc") {
		t.Errorf("published image not imported:\n%s", out)
	}
	if out := dryRun(t, "lint", "--dry-run", "-f", recipe); strings.Contains(out, "-cache ") {
		t.Errorf("lint with a cache:\n%s", out)
	}
	var r refs
	if r.Set("a,b") == nil || r.Set("") == nil || r.Set("ghcr.io/a/b:c") != nil {
		t.Error("refs")
	}
}

func TestLintAndResolveGoThroughBuildKit(t *testing.T) {
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	out := dryRun(t, "lint", "--dry-run", "--pull", "always", "-f", recipe)
	for _, want := range []string{"buildkitd.sock build --frontend gateway.v0", "--opt mode=lint", "--opt image-resolve-mode=pull", "--output type=local,dest="} {
		if !strings.Contains(out, want) {
			t.Errorf("lint: missing %q in:\n%s", want, out)
		}
	}
	out = dryRun(t, "recipe", "export", "--dry-run", recipe)
	if !strings.Contains(out, "--opt mode=resolve") || strings.Contains(out, "podman run --rm") {
		t.Errorf("export of a recipe file:\n%s", out)
	}
}

func TestSystemRecipeByDefault(t *testing.T) {
	t.Setenv("NOMIRA_RECIPE", "")
	old := systemOutput
	systemOutput = filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { systemOutput = old })
	out := dryRun(t, "build", "--dry-run")
	for _, want := range []string{"--local dockerfile=/etc/nomira", "filename=recipe.yml", "dest=" + systemOutput + "/.build/system.oci,"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	t.Setenv("NOMIRA_RECIPE", recipe)
	out = dryRun(t, "build", "--dry-run")
	if !strings.Contains(out, "--local dockerfile="+filepath.Dir(recipe)) || !strings.Contains(out, filepath.Join(filepath.Dir(recipe), ".nomira", ".build", "mysystem.oci")) {
		t.Errorf("NOMIRA_RECIPE not used:\n%s", out)
	}
}

func TestFlagsShapeTheBuild(t *testing.T) {
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	t.Setenv("BUILDKIT_HOST", "unix:///run/buildkit/buildkitd.sock")
	var buf bytes.Buffer
	r := &runner{dryRun: true, out: &buf}
	o := &options{recipe: recipe, frontend: "ghcr.io/x/front:9", progress: "plain", compression: "uncompressed", pull: "always",
		name: "sys", secrets: secrets{"id=abs,src=/etc/kernel/db.crt"}}
	if _, err := build(r, o); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"--secret id=abs,src=/etc/kernel/db.crt",
		"--opt image-resolve-mode=pull",
		"--progress plain",
		".build/sys.oci,compression=uncompressed,rewrite-timestamp=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	o.compression = "lz4"
	if _, err := build(r, o); err == nil {
		t.Error("compression lz4 accepted")
	}
}

func TestConf(t *testing.T) {
	recipe := writeRecipe(t, "# syntax=ghcr.io/x/front:9\nsettings: {}\n")
	dir := filepath.Dir(recipe)
	conf := "# build settings\nname = laptop\ncompression = gzip  # smaller\nsecret = id=db.key,src=keys/db.key\nsecret = id=tok,src=/run/tok\npull = always\n"
	if err := os.WriteFile(filepath.Join(dir, confName), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUILDKIT_HOST", "unix:///run/buildkit/buildkitd.sock")
	out := dryRun(t, "build", "--dry-run", "-f", recipe, "--compression", "zstd")
	for _, want := range []string{
		"--secret id=db.key,src=" + filepath.Join(dir, "keys/db.key"),
		"--secret id=tok,src=/run/tok",
		"--opt image-resolve-mode=pull",
		"compression=zstd", // the command line wins
		"laptop.oci",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// lint has no --compression: skipped there, not an error.
	_ = dryRun(t, "lint", "--dry-run", "-f", recipe)

	for name, bad := range map[string]string{"typo": "compresion = zstd\n", "recipe": "f = x.yml\n", "no value": "name\n"} {
		if err := os.WriteFile(filepath.Join(dir, confName), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		var o, e bytes.Buffer
		if code := runCLI([]string{"build", "--dry-run", "-f", recipe}, &o, &e); code != 2 {
			t.Errorf("%s: exit %d, %s", name, code, e.String())
		}
	}
}

func TestCompatibility(t *testing.T) {
	for _, c := range []struct{ cli, fe, want string }{
		{"0.4.1", "0.4.7", ""},
		{"0.4.1-beta.2", "0.4.0", ""},
		{"0.4.1", "0.5.0", "differ in major.minor"},
		{"1.0.0", "0.9.0", "differ in major.minor"},
		{"sha-1a2b3c4", "0.4.0", "not both releases"},
		{"0.4.0", "sha-1a2b3c4", "not both releases"},
		{"0.4.0", "", "unknown"},
		{"0.0.0-20261127123456-0123456789ab", "0.0.0", "not both releases"},
	} {
		got := compatibility(c.cli, c.fe)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s vs %s: %q", c.cli, c.fe, got)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := strings.Split("settings:\n  name: a\nbuild:\n  packages:\n    collect: run x\n  features:\n    groups:\n      g: [run a]", "\n")
	b := strings.Split("settings:\n  name: a\nbuild:\n  packages:\n    collect: run x\n  features:\n    groups:\n      g: [run b]\n      h: [run h]", "\n")
	got := unifiedDiff("booted", "recipe.yml", a, b, 1)
	want := "--- booted\n+++ recipe.yml\n@@ -7,2 +7,3 @@\n     groups:\n-      g: [run a]\n+      g: [run b]\n+      h: [run h]\n"
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if unifiedDiff("a", "b", a, a, 3) != "" {
		t.Error("equal recipes differ")
	}
}

func TestRecipeSources(t *testing.T) {
	if got := skopeoRef("registry", "ghcr.io/x/y:1"); got != "docker://ghcr.io/x/y:1" {
		t.Errorf("registry: %s", got)
	}
	if got := skopeoRef("oci", "/var/lib/nomira/x.oci"); got != "oci:/var/lib/nomira/x.oci" {
		t.Errorf("oci: %s", got)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isLayoutDir(dir) || isRecipeFile(dir) {
		t.Error("layout dir not recognised")
	}
	var buf bytes.Buffer
	r := &runner{dryRun: true, out: &buf}
	if _, err := recipeText(r, &options{}, "nonsense"); err == nil {
		t.Error("nonsense accepted as a source")
	}
}

func TestRecipeCommandUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCLI([]string{"recipe"}, &out, &errb); code != 2 {
		t.Errorf("nomira recipe: exit %d", code)
	}
	if code := runCLI([]string{"recipe", "export", "a", "b"}, &out, &errb); code != 2 {
		t.Errorf("two sources for export: exit %d", code)
	}
}

func TestLogrusMessage(t *testing.T) {
	in := `time="2026-11-27T16:33:28Z" level=warning msg="failed check for fsverity support" error="enable fsverity failed: operation \"x\"" path=a`
	if got := logrusMessage(in); got != `failed check for fsverity support: enable fsverity failed: operation "x"` {
		t.Errorf("got %q", got)
	}
	if got := logrusMessage("error: failed to solve"); got != "error: failed to solve" {
		t.Errorf("plain line changed: %q", got)
	}
}
