# AGENTS.md

Guidance for coding agents (and humans) working in this repository.

## What this is

`nomira` is the command line companion to the
[nomira](https://github.com/Nomira-OS/nomira) BuildKit frontend. It builds
a recipe into an OCI directory that bootc follows, checks recipes and
shows the recipe an image was built from. It is a thin wrapper around
`buildctl`; `podman` only runs a buildkitd it manages, `skopeo` and
`bootc` only serve `nomira recipe` sources.

Sibling repositories: `nomira` (the frontend), `arch-base` (the base
image, which ships this CLI), `nomira-web` (reference recipe and website).

## Layout

- `main.go`: command dispatch and usage.
- `commands.go`: `build`, `lint`, the buildctl call they share, and the flags.
- `recipecmd.go`: `recipe export` and `recipe diff`.
- `layout.go`: OCI layout directories: labels, pruning, publishing.
- `buildkit.go`: finds or starts buildkitd.
- `recipe.go`: `# syntax=` detection.
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
  frontend through BuildKit (`--opt mode=lint|resolve`) instead, or read
  the labels it writes into images.
- **Every external command goes through `runner`**, so `--dry-run` shows
  the complete, truthful sequence. Read-only probes use `runner.query`
  and need a fallback, because dry-run skips them.
- **Publishing is a safety property:** bootc follows the output
  directory, so nothing reaches it before the build has succeeded, and
  `index.json` is replaced last. `TestBuildDryRunOrder` and `TestPublish`
  guard this; keep them passing.
- **No commands that change the running system.** Deploying is bootc's.
- **Versions are pinned.** Never use `latest` or another moving tag for
  images, runners or actions. Renovate keeps pins current.
- **CI is shared.** The workflows here only call the reusable workflows
  in `Nomira-OS/ci`; change CI behaviour there, not here.
- **Conventional Commits** for every commit (`feat:`, `fix:`, `docs:` …,
  `!` for breaking changes). Version and changelog are derived from them.
  Releases are cut by hand with the `release` workflow (beta or final);
  never push a tag yourself.
- **Licence:** EUPL-1.2, declared through `REUSE.toml`; `reuse lint` must
  pass.
- **Language:** everything is English: README, docs, code, comments and
  commit messages.
- **Be explicit about what was not verified.** Tests only cover the
  dry-run sequence and the layout code; they do not run buildkitd,
  skopeo or bootc.
