// nomira is the command line companion to the nomira BuildKit frontend.
// It knows the frontend only as a container image that BuildKit runs:
// build, lint and resolving a recipe all go through buildctl.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const usage = `nomira — build bootc images from nomira recipes; bootc upgrade takes them
(alpha: commands, flags and the recipe schema may still change)

usage:
  nomira build    [flags]          build the recipe into an OCI directory
  nomira lint     [flags]          check the recipe and its imports
  nomira recipe export [-o FILE] [SOURCE]
                                   print or save the recipe an image was built from
  nomira recipe diff [--origin] [A [B]]
                                   compare two recipes (default: booted, then -f)
  nomira version                   print the version

A SOURCE is "booted" (the image the system runs; the default), a recipe
file, an OCI layout directory or an image reference (docker://…, oci:…,
containers-storage:…). Images carry their resolved recipe in the label
org.nomira.recipe.

Run "nomira <command> -h" for the flags of a command. Flags can also be
set in nomira.conf next to the recipe, one "flag = value" per line.

environment:
  BUILDKIT_HOST          use this buildkitd; otherwise a local socket is
                         used if one answers, else a managed podman container
  NOMIRA_BUILDKIT_IMAGE  image for the managed buildkitd container
  NOMIRA_RECIPE          recipe to use without -f (default: /etc/nomira/recipe.yml)
  NOMIRA_CONFIG          the nomira.conf to read (default: next to the recipe)
  NOMIRA_LOG_FORMAT      text or json, as --log-format
  NOMIRA_COLOR           auto, always or never, as --color; NO_COLOR turns colour off
`

// removedCommands are commands of earlier versions, with where they went.
var removedCommands = map[string]string{
	"validate": "checks are steps of the recipe, such as run bootc container lint; a failing one stops the build before anything is published",
	"graph":    "the build output shows the phases and groups, and nomira recipe export the resolved recipe",
}

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
		fmt.Fprintln(stdout, "nomira", cliVersion())
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
	if why, ok := removedCommands[cmd]; ok {
		fmt.Fprintf(stderr, "nomira: %s is gone: %s\n", cmd, why)
		return 2
	}
	switch cmd {
	case "build", "lint", "recipe export", "recipe diff":
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
	if path, required := o.confPath(); true {
		if err := applyConf(fs, path, required, allFlags()); err != nil {
			fmt.Fprintln(stderr, "nomira:", err)
			return 2
		}
	}
	switch o.pull {
	case "always", "missing", "never":
	default:
		fmt.Fprintf(stderr, "nomira: --pull: %q is not always, missing or never\n", o.pull)
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
			err = recipeDiff(r, &o, a, b, stdout)
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
		case "lint":
			err = lint(r, &o)
		case "build":
			var dir string
			if dir, err = buildAndPublish(r, &o); err == nil && !o.dryRun {
				fmt.Fprintln(stdout, dir)
			}
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

// allFlags names the flags of every command, so that nomira.conf can tell
// a flag of another command from a typo.
func allFlags() map[string]bool {
	known := map[string]bool{}
	for _, cmd := range []string{"build", "lint", "recipe export", "recipe diff"} {
		var o options
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		o.register(fs, cmd)
		fs.VisitAll(func(f *flag.Flag) { known[f.Name] = true })
	}
	return known
}
