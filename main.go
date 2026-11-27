// nomira is the command line companion to the nomira BuildKit frontend.
// It knows the frontend only as a container image: building goes through
// buildctl, and lint/graph run the image's helper modes via podman.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// version of the CLI. nomira is in alpha: commands, flags and the recipe
// schema may still change without notice.
const version = "0.1.0-alpha"

const usage = `nomira — build bootc images from nomira recipes; bootc upgrade takes them
(alpha: commands, flags and the recipe schema may still change)

usage:
  nomira build    [flags]          build the recipe and check the image
  nomira validate [flags] [image]  run validation.d hooks and the boot test
  nomira lint     [flags]          check the recipe
  nomira graph    [flags]          print the pipeline as a Mermaid flowchart
  nomira recipe export [-o FILE] [SOURCE]
                                   print or save the recipe an image was built from
  nomira recipe diff [--origin] [A [B]]
                                   compare two recipes (default: booted, then -f)
  nomira version                   print the version

A SOURCE is "booted" (the image the system runs; the default), a recipe
file, an OCI layout directory or an image reference (docker://…, oci:…,
containers-storage:…). Images carry their resolved recipe in the label
org.nomira.recipe.

Run "nomira <command> -h" for the flags of a command.

environment:
  BUILDKIT_HOST          use this buildkitd; otherwise a local socket is
                         used if one answers, else a managed podman container
  NOMIRA_BUILDKIT_IMAGE  image for the managed buildkitd container
  NOMIRA_RECIPE          recipe to use without -f (default: /etc/nomira/recipe.yml)
  NOMIRA_LOG_FORMAT      text or json, as --log-format
  NOMIRA_COLOR           auto, always or never, as --color; NO_COLOR turns colour off
`

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" || argv[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd := argv[0]
	if cmd == "version" || cmd == "--version" {
		fmt.Fprintln(stdout, "nomira", version)
		return 0
	}
	if cmd == "recipe" {
		if len(argv) < 2 || (argv[1] != "export" && argv[1] != "diff") {
			fmt.Fprintf(stderr, "nomira: recipe needs export or diff\n\n%s", usage)
			return 2
		}
		cmd = "recipe " + argv[1]
		argv = argv[1:]
	}
	switch cmd {
	case "build", "validate", "lint", "graph", "recipe export", "recipe diff":
	default:
		fmt.Fprintf(stderr, "nomira: unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	var o options
	fs := flag.NewFlagSet("nomira "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	o.register(fs, cmd)
	positional, err := parseInterleaved(fs, argv[1:])
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	r := &runner{dryRun: o.dryRun, out: stderr, start: time.Now()}
	if o.dryRun {
		r.out, r.start = stdout, time.Time{}
	}
	log, err := newLogger(r.out, o.logFormat, o.color, r.start)
	if err != nil {
		fmt.Fprintln(stderr, "nomira:", err)
		return 2
	}
	r.log = log

	if strings.HasPrefix(cmd, "recipe ") {
		args := positional
		if cmd == "recipe export" {
			if len(args) > 1 {
				fmt.Fprintln(stderr, "nomira: recipe export takes one source")
				return 2
			}
			src := sourceBooted
			if len(args) == 1 {
				src = args[0]
			}
			err = recipeExport(r, &o, src, stdout)
		} else {
			if len(args) > 2 {
				fmt.Fprintln(stderr, "nomira: recipe diff takes two sources")
				return 2
			}
			a, b := sourceBooted, o.recipe
			if len(args) > 0 {
				a = args[0]
			}
			if len(args) > 1 {
				b = args[1]
			}
			err = recipeDiff(r, a, b, o.origin, stdout)
		}
		if err != nil {
			log.fail(stderr, err)
			return 1
		}
		return 0
	}

	err = o.resolve(r)
	if err == nil {
		switch cmd {
		case "lint", "graph":
			err = frontendMode(r, &o, cmd)
		case "build":
			var dir string
			if dir, err = buildAndCheck(r, &o); err == nil && !o.dryRun {
				fmt.Fprintln(stdout, dir)
			}
		case "validate":
			ref := ""
			if len(positional) > 0 {
				ref = positional[0]
			}
			if ref == "" {
				ref = o.imageRef(r)
			}
			err = validate(r, &o, ref)
		}
	}
	if err != nil {
		log.fail(stderr, err)
		return 1
	}
	return 0
}

// parseInterleaved parses flags before, between and after the positional
// arguments, so that "nomira recipe diff a.yml b.yml --color never" works.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		if args[0] == "--" {
			return append(positional, args[1:]...), nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
