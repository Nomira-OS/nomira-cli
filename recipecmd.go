package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// nomira recipe export|diff: the recipe an image was built from.
//
// The nomira frontend writes the resolved recipe, with all imports merged,
// into the image's label org.nomira.recipe. A source is one of
//
//	booted        the image the running system booted (bootc status)
//	FILE.yml      a recipe, resolved by its frontend through BuildKit
//	DIR           an OCI layout directory, such as nomira build's output
//	TRANSPORT:REF any image skopeo can read: oci:…, docker://…,
//	              containers-storage:…, oci-archive:…
//
// Two resolved recipes compare line by line: imports cannot get in the
// way, both sides are complete.

const sourceBooted = "booted"

// recipeText returns the resolved recipe of a source. o gives the flags
// for asking a frontend (pull policy, progress).
func recipeText(r *runner, o *options, src string) (string, error) {
	switch {
	case src == sourceBooted:
		ref, err := bootedRef(r)
		if err != nil {
			return "", err
		}
		if dir, ok := strings.CutPrefix(ref, "oci:"); ok && isLayoutDir(dir) {
			labels, err := layoutLabels(dir)
			if err != nil {
				return "", err
			}
			return recipeOf(ref, labels)
		}
		return labelOf(r, ref)
	case isRecipeFile(src):
		ro := *o
		ro.recipe = src
		if err := ro.resolve(r); err != nil {
			return "", err
		}
		dir, err := ask(r, &ro, "resolve")
		if err != nil || r.dryRun {
			return "", err
		}
		defer os.RemoveAll(dir)
		out, err := os.ReadFile(filepath.Join(dir, "recipe.yml"))
		if err != nil {
			return "", fmt.Errorf("%s: the frontend did not resolve the recipe (one older than mode=resolve?)", src)
		}
		return string(out), nil
	case isLayoutDir(src):
		labels, err := layoutLabels(src)
		if err != nil {
			return "", err
		}
		return recipeOf(src, labels)
	case strings.Contains(src, ":"):
		return labelOf(r, src)
	}
	return "", fmt.Errorf("%s: neither a recipe file, an OCI layout directory nor an image reference", src)
}

func isRecipeFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml"))
}

func isLayoutDir(p string) bool {
	_, err := os.Stat(filepath.Join(p, "index.json"))
	return err == nil
}

// labelOf reads the recipe label of an image elsewhere with skopeo.
func labelOf(r *runner, ref string) (string, error) {
	if err := r.need("skopeo"); err != nil {
		return "", err
	}
	out, ok := r.query("skopeo", "inspect", "--config", ref)
	if !ok {
		return "", fmt.Errorf("cannot read the image config of %s", ref)
	}
	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		return "", fmt.Errorf("%s: %w", ref, err)
	}
	return recipeOf(ref, cfg.Config.Labels)
}

func recipeOf(ref string, labels map[string]string) (string, error) {
	text, ok := labels[recipeLabel]
	if !ok {
		return "", fmt.Errorf("%s has no label %s (built without nomira, with an older frontend, or with record: false)", ref, recipeLabel)
	}
	return text, nil
}

// bootedRef finds the image the system booted and makes sure skopeo
// still finds that very image there: an OCI directory, for one, holds
// only the newest build, which need not be the booted one.
func bootedRef(r *runner) (string, error) {
	if err := r.need("bootc"); err != nil {
		return "", err
	}
	out, ok := r.query("bootc", "status", "--json")
	if !ok {
		return "", fmt.Errorf("bootc status failed; is this a bootc system?")
	}
	var st struct {
		Status struct {
			Booted *struct {
				Image *struct {
					Image struct {
						Image     string `json:"image"`
						Transport string `json:"transport"`
					} `json:"image"`
					ImageDigest string `json:"imageDigest"`
				} `json:"image"`
			} `json:"booted"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return "", fmt.Errorf("bootc status: %w", err)
	}
	b := st.Status.Booted
	if b == nil || b.Image == nil {
		return "", fmt.Errorf("bootc status names no booted image")
	}
	ref := skopeoRef(b.Image.Image.Transport, b.Image.Image.Image)
	if dir, ok := strings.CutPrefix(ref, "oci:"); ok && isLayoutDir(dir) {
		if d := layoutDigest(dir); d != "" && b.Image.ImageDigest != "" && d != b.Image.ImageDigest {
			return "", fmt.Errorf("the booted image %s is no longer in %s, which now holds %s (a newer build, not yet booted?)", b.Image.ImageDigest, dir, d)
		}
		return ref, nil
	}
	if err := r.need("skopeo"); err != nil {
		return "", err
	}
	if out, ok := r.query("skopeo", "inspect", ref); ok {
		var m struct {
			Digest string `json:"Digest"`
		}
		if json.Unmarshal([]byte(out), &m) == nil && m.Digest != "" && b.Image.ImageDigest != "" && m.Digest != b.Image.ImageDigest {
			return "", fmt.Errorf("the booted image %s is no longer at %s, which now holds %s (a newer build, not yet booted?)", b.Image.ImageDigest, ref, m.Digest)
		}
	}
	return ref, nil
}

// skopeoRef turns bootc's transport and image into a skopeo reference.
func skopeoRef(transport, image string) string {
	switch transport {
	case "", "registry":
		return "docker://" + image
	case "oci", "oci-archive", "containers-storage", "docker-archive", "dir":
		return transport + ":" + image
	}
	return transport + ":" + image
}

// recipeExport writes the recipe of src to out, or to the file o.output.
func recipeExport(r *runner, o *options, src string, stdout io.Writer) error {
	text, err := recipeText(r, o, src)
	if err != nil || r.dryRun {
		return err
	}
	if o.output == "" {
		_, err := io.WriteString(stdout, text)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.output), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(o.output, []byte(text), 0o644); err != nil {
		return err
	}
	r.logger().info("wrote " + o.output)
	return nil
}

// recipeDiff prints a unified diff of the recipes of a and b. Unless
// origin is set, it leaves out where each part came from (the "# from"
// comments and the list of files): moving a group into another file
// changes nothing in the image.
func recipeDiff(r *runner, o *options, a, b string, stdout io.Writer) error {
	origin := o.origin
	ta, err := recipeText(r, o, a)
	if err != nil {
		return err
	}
	tb, err := recipeText(r, o, b)
	if err != nil || r.dryRun {
		return err
	}
	la, lb := splitLines(ta), splitLines(tb)
	if !origin {
		la, lb = withoutOrigin(la), withoutOrigin(lb)
	}
	d := unifiedDiff(a, b, la, lb, 3)
	if d == "" {
		r.logger().info("the recipes are the same")
		return nil
	}
	_, err = io.WriteString(stdout, r.logger().colorDiff(d, r.logger().colorOn(stdout)))
	return err
}

// withoutOrigin drops the resolver's notes on where things came from.
func withoutOrigin(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(l, "# Resolved by nomira from ") {
			continue
		}
		if i := strings.LastIndex(l, " # from "); i >= 0 {
			l = l[:i]
		}
		out = append(out, l)
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// unifiedDiff compares two lists of lines (longest common subsequence;
// recipes are small) and renders the differences with ctx lines of
// context, as diff -u does.
func unifiedDiff(nameA, nameB string, a, b []string, ctx int) string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
		ai   int // line in a (1-based) before or at this op
		bi   int
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, op{' ', a[i], i + 1, j + 1})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', a[i], i + 1, j + 1})
			i++
		default:
			ops = append(ops, op{'+', b[j], i + 1, j + 1})
			j++
		}
	}
	changed := false
	for _, o := range ops {
		changed = changed || o.kind != ' '
	}
	if !changed {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", nameA, nameB)
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		// A hunk: from ctx lines before the change to ctx lines after the
		// last change that is at most 2*ctx lines away.
		start := max(0, k-ctx)
		end := k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			next := end
			for next < len(ops) && ops[next].kind == ' ' {
				next++
			}
			if next == len(ops) || next-end > 2*ctx {
				end = min(len(ops), end+ctx)
				break
			}
			end = next
		}
		la, lb := 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				la++
			}
			if o.kind != '-' {
				lb++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", ops[start].ai, la, ops[start].bi, lb)
		for _, o := range ops[start:end] {
			out.WriteString(string(o.kind) + o.text + "\n")
		}
		k = end
	}
	return out.String()
}

// colorDiff colours a unified diff for a terminal.
func (l *logger) colorDiff(d string, color bool) string {
	if !color {
		return d
	}
	paint := func(style, s string) string { return "\x1b[" + style + "m" + s + "\x1b[0m" }
	lines := strings.SplitAfter(d, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			lines[i] = paint(styleBold, strings.TrimSuffix(line, "\n")) + "\n"
		case strings.HasPrefix(line, "@@"):
			lines[i] = paint(styleCyan, strings.TrimSuffix(line, "\n")) + "\n"
		case strings.HasPrefix(line, "-"):
			lines[i] = paint(styleRed, strings.TrimSuffix(line, "\n")) + "\n"
		case strings.HasPrefix(line, "+"):
			lines[i] = paint(styleGreen, strings.TrimSuffix(line, "\n")) + "\n"
		}
	}
	return strings.Join(lines, "")
}

// layoutDigest is the digest of the manifest index.json of a layout
// directory names first, as bootc reports it for an oci image.
func layoutDigest(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return ""
	}
	var index ociDoc
	if json.Unmarshal(data, &index) != nil || len(index.Manifests) == 0 {
		return ""
	}
	return index.Manifests[0].Digest
}
