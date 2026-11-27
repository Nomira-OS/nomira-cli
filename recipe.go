package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// syntaxImage returns the frontend image named by a leading `# syntax=`
// comment, or "" if there is none. Only leading comment lines are
// considered, as in Dockerfiles.
func syntaxImage(recipe string) string {
	f, err := os.Open(recipe)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			return ""
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if k, v, ok := strings.Cut(body, "="); ok && strings.TrimSpace(strings.ToLower(k)) == "syntax" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// frontendArgs builds a `podman run` invocation of the frontend image in
// one of its helper modes (lint, graph, inspect) with the recipe directory
// mounted read-only, so includes resolve.
func frontendArgs(image, recipe, mode string) []string {
	dir, file := filepath.Split(recipe)
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return []string{
		"run", "--rm",
		"--security-opt", "label=disable",
		"-v", abs + ":/recipe:ro",
		image, mode, "/recipe/" + file,
	}
}

// recipeInfo holds the settings the CLI acts on, as the frontend reports
// them (inspect).
type recipeInfo struct {
	Name        string            `json:"name"`
	Compression string            `json:"compression"`
	Secrets     map[string]string `json:"secrets"`
}

// inspectRecipe asks the frontend for the recipe's settings. Without an
// answer (in a dry run, say) the name falls back to the recipe directory's
// name and the compression to zstd.
func inspectRecipe(r *runner, image, recipe string) *recipeInfo {
	info := &recipeInfo{}
	if out, ok := r.query("podman", frontendArgs(image, recipe, "inspect")...); ok {
		_ = json.Unmarshal([]byte(out), info)
	}
	if info.Name == "" {
		info.Name = "nomira"
		if abs, err := filepath.Abs(filepath.Dir(recipe)); err == nil {
			info.Name = strings.ToLower(filepath.Base(abs))
		}
	}
	if info.Compression == "" {
		info.Compression = "zstd"
	}
	return info
}
