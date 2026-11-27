package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runner executes external tools. In dry-run mode it only prints the
// commands, so the whole workflow can be inspected without side effects.
type runner struct {
	dryRun bool
	out    io.Writer // where the log goes, unless log is set
	start  time.Time // when the CLI started; zero in tests
	log    *logger
}

// logger returns the runner's logger; by default text on out.
func (r *runner) logger() *logger {
	if r.log == nil {
		r.log, _ = newLogger(r.out, formatText, "never", r.start)
		if r.dryRun {
			r.log.start = time.Time{}
		}
	}
	return r.log
}

func clock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func (r *runner) echo(env []string, name string, args []string) {
	r.logger().command(env, name, args)
}

// run executes a command with inherited stdio.
func (r *runner) run(name string, args ...string) error {
	return r.runEnv(nil, name, args...)
}

// runEnv is run with extra KEY=VALUE environment entries.
func (r *runner) runEnv(env []string, name string, args ...string) error {
	return r.exec(env, name, args, "")
}

// Ways to show buildctl's progress.
const (
	viewNomira = "nomira" // rawjson, rendered by nomira as text
	viewEvents = "events" // rawjson, turned into JSON log events
	viewRaw    = "raw"    // buildctl's own display, between two rules
)

// runFramed runs a tool whose own output is long, buildctl, and shows its
// progress as view says.
func (r *runner) runFramed(view, name string, args ...string) error {
	return r.exec(nil, name, args, view)
}

func (r *runner) exec(env []string, name string, args []string, view string) error {
	l := r.logger()
	l.command(env, name, args)
	if r.dryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	var pw *io.PipeWriter
	var events chan struct{}
	switch view {
	case viewNomira, viewEvents:
		// Progress goes to stderr; stdout carries nothing nomira needs.
		var pr *io.PipeReader
		pr, pw = io.Pipe()
		cmd.Stdout, cmd.Stderr = os.Stderr, pw
		events = make(chan struct{})
		go func() {
			if view == viewEvents {
				l.buildkitEvents(pr)
			} else {
				l.buildkitText(pr)
			}
			close(events)
		}()
	case viewRaw:
		l.rule(name)
	}
	began := time.Now()
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("%s: %w", name, err)
	}
	if pw != nil {
		pw.Close()
		<-events
	} else if view == viewRaw {
		l.rule("")
	}
	l.finished(name, time.Since(began), err)
	return err
}

// query runs a read-only command and returns its stdout. It is skipped in
// dry-run mode (ok=false), so callers must have a fallback.
func (r *runner) query(name string, args ...string) (out string, ok bool) {
	if r.dryRun {
		return "", false
	}
	// Shown like any other step: a query can take a while, podman pulling
	// the frontend image the first time for instance, and its progress
	// and errors on stderr are the only sign of life.
	r.logger().command(nil, name, args)
	var buf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
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
	"bootc":    "bootc",
	"buildctl": "buildkit",
	"podman":   "podman",
	"skopeo":   "skopeo",
}
