package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// runner executes external tools. In dry-run mode it only prints the
// commands, so the whole workflow can be inspected without side effects.
type runner struct {
	dryRun bool
	out    io.Writer // where "+ cmd" lines go
}

func (r *runner) echo(env []string, name string, args []string) {
	parts := append(append([]string{}, env...), name)
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'$") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	fmt.Fprintln(r.out, "+ "+strings.Join(parts, " "))
}

// run executes a command with inherited stdio.
func (r *runner) run(name string, args ...string) error {
	return r.runEnv(nil, name, args...)
}

// runEnv is run with extra KEY=VALUE environment entries.
func (r *runner) runEnv(env []string, name string, args ...string) error {
	r.echo(env, name, args)
	if r.dryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// query runs a read-only command and returns its stdout. It is skipped in
// dry-run mode (ok=false), so callers must have a fallback.
func (r *runner) query(name string, args ...string) (out string, ok bool) {
	if r.dryRun {
		return "", false
	}
	var buf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return strings.TrimSpace(buf.String()), true
}

// need fails with an install hint when a tool is missing.
func (r *runner) need(tools ...string) error {
	if r.dryRun {
		return nil
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("%s not found in PATH (Arch: pacman -S %s)", t, archPackage[t])
		}
	}
	return nil
}

var archPackage = map[string]string{
	"buildctl": "buildkit",
	"podman":   "podman",
	"skopeo":   "skopeo",
}
