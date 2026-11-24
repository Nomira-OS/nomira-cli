// nomira is the command line companion to the nomira BuildKit frontend.
// It knows the frontend only as a container image: building goes through
// buildctl, and lint/graph run the image's helper modes via podman.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// version of the CLI. nomira is in alpha: commands, flags and the recipe
// schema may still change without notice.
const version = "0.1.0-alpha"

const usage = `nomira — build and switch to bootc images from nomira recipes
(alpha: commands, flags and the recipe schema may still change)

usage:
  nomira build    [flags]          build the recipe into an OCI layout directory
  nomira switch   [flags]          build, validate, then bootc switch
  nomira validate [flags] [image]  run validation.d hooks and the boot test
  nomira lint     [flags]          check the recipe
  nomira graph    [flags]          print the pipeline as a Mermaid flowchart
  nomira version                   print the version

Run "nomira <command> -h" for the flags of a command.

environment:
  BUILDKIT_HOST          use this buildkitd; otherwise a local socket is
                         used if one answers, else a managed podman container
  NOMIRA_BUILDKIT_IMAGE  image for the managed buildkitd container
  NOMIRA_RECIPE          recipe to use without -f (default: /etc/nomira/recipe.yml)
  NOMIRA_SUDO            command used to become root (default: sudo)
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
	switch cmd {
	case "build", "switch", "validate", "lint", "graph":
	default:
		fmt.Fprintf(stderr, "nomira: unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	var o options
	fs := flag.NewFlagSet("nomira "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	o.register(fs, cmd)
	if err := fs.Parse(argv[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	r := &runner{dryRun: o.dryRun, out: stderr, start: time.Now()}
	if o.dryRun {
		r.out = stdout
	}

	err := o.resolve(r)
	if err == nil {
		switch cmd {
		case "lint", "graph":
			err = frontendMode(r, &o, cmd)
		case "build":
			var dir string
			if dir, err = build(r, &o); err == nil && !o.dryRun {
				fmt.Fprintln(stdout, dir)
			}
		case "validate":
			ref := fs.Arg(0)
			if ref == "" {
				ref = o.imageRef(r)
			}
			err = validate(r, &o, ref)
		case "switch":
			err = switchTo(r, &o)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "nomira:", err)
		return 1
	}
	return 0
}
