# AGENTS.md

Guidance for coding agents (and humans) working in this repository.

## What this is

`nomira` is the command line companion to the
[nomira](https://github.com/Nomira-OS/nomira) BuildKit frontend. It builds
a recipe, validates the image and switches the running system to it with
bootc. It is a thin wrapper around `buildctl`, `podman`, `skopeo`, `bcvk`
and `bootc`.

Sibling repositories: `nomira` (the frontend), `nomira-image` (the base
image, which ships this CLI), `nomira-web` (reference recipe and website).

## Layout

- `main.go`: command dispatch and usage.
- `commands.go`: `build`, `validate`, `switch`, `lint`, `graph` and their flags.
- `buildkit.go`: finds or starts buildkitd.
- `recipe.go`: `# syntax=` detection and calls into the frontend image.
- `runner.go`: runs external tools; implements `--dry-run`.

## Commands

```sh
gofmt -l .        # must print nothing
go vet ./...
go test ./...
go build -o nomira .
```

## Rules

- **Standard library only.** No third-party Go modules; that is why there
  is no `go.sum`.
- **The frontend is an image, not a library.** Never import code from the
  `nomira` repository and never parse the recipe schema here. Ask the
  frontend image (`lint`, `graph`, `inspect`) instead.
- **Every external command goes through `runner`**, so `--dry-run` shows
  the complete, truthful sequence. Read-only probes use `runner.query`
  and need a fallback, because dry-run skips them.
- **Order in `switch` is a safety property:** nothing may reach root's
  storage or bootc before the validation hooks and the boot test have
  passed. `TestSwitchDryRunOrder` guards this; keep it passing and extend
  it when the sequence changes.
- **Versions are pinned.** Never use `latest` or another moving tag for
  images, runners or actions. GitHub Actions are pinned to a commit SHA
  with the version as a trailing comment. Renovate keeps pins current.
- **Languages:** README is German. Code, comments, commit messages and
  this file are English.
- **Be explicit about what was not verified.** Tests only cover the
  dry-run sequence; they do not run buildkitd, skopeo, bcvk or bootc.
