package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// defaultFrontend is used when the recipe has no `# syntax=` line.
const defaultFrontend = "ghcr.io/nomira-os/nomira:1"

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

// imageName asks the frontend for settings.name and falls back to the
// recipe directory's name.
func imageName(r *runner, image, recipe string) string {
	if out, ok := r.query("podman", frontendArgs(image, recipe, "inspect")...); ok {
		var info struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(out), &info) == nil && info.Name != "" {
			return info.Name
		}
	}
	abs, err := filepath.Abs(filepath.Dir(recipe))
	if err != nil {
		return "nomira-image"
	}
	return strings.ToLower(filepath.Base(abs))
}
