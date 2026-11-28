# nomira-cli

> **Alpha.** Commands, flags and the recipe schema may still change
> without notice. `nomira version` shows what you have.

The command line for [nomira](https://github.com/Nomira-OS/nomira): it
builds a recipe and publishes the image to a directory that bootc
follows. Deploying is bootc's: `bootc upgrade` takes the new image.

The CLI knows the frontend only as a container image that BuildKit runs:
`build`, `lint` and resolving a recipe all go through `buildctl`. The
frontend therefore stays usable without this CLI, and the CLI needs no
container runtime except for a buildkitd it manages itself.

## Commands

```
nomira build    [flags]          build a recipe and publish the image
nomira lint     [flags]          check a recipe and its imports
nomira recipe export [-o FILE] [SOURCE]   the recipe an image was built from
nomira recipe diff [--origin] [A [B]]     compare two recipes
```

Flags (`nomira <command> -h` shows all of them):

| Flag | Meaning |
|---|---|
| `-f recipe.yml` | entry file of the recipe; default `$NOMIRA_RECIPE`, else `/etc/nomira/recipe.yml` |
| `-o DIR` | output directory (see below for the default) |
| `--dry-run` | only print the commands, run nothing |
| `--progress plain` | buildctl's own progress display (`plain`, `tty`, `auto`) instead of nomira's (`nomira`, the default) |
| `--log-format json` | output for log collectors, see below; default `text` (`$NOMIRA_LOG_FORMAT`) |
| `--color never` | colour in text output: `auto` (default: on a terminal, unless `NO_COLOR` is set), `always`, `never` (`$NOMIRA_COLOR`) |
| `--name NAME` | image name, for the output directory; default `system` for the recipe in `/etc`, else the name of the recipe's directory |
| `--compression gzip` | layer compression: `zstd` (default), `gzip`, `uncompressed` |
| `--secret id=ID,src=FILE` | a secret the recipe mounts; may be repeated |
| `--pull always` | images: `always` pull again (a moved tag such as `latest`), pull `missing` ones (default), `never` pull |
| `--cache-from REF` | an image in a registry whose steps may be taken over, such as the one CI built from the same recipe; may be repeated |
| `--no-check` | skip the conflict checks between groups and between buckets: faster, less safe (`no-check = true` in `nomira.conf`) |
| `--config FILE` | the `nomira.conf` to read, see below |

Flags may come before or after the other arguments.

**Recipe or flag?** What a frontend can act on is in the recipe: base
image, phases, mounts, `settings.source_date_epoch` and so on. What only
the BuildKit client can set is a flag: the image name, the layer
compression, the files behind secrets, the pull policy, the output. The rule: what has to be a
client option does not belong in the recipe. Timestamps are always
clamped to the recipe's source date epoch (`rewrite-timestamp=true`).

## nomira.conf

Flags for repeated runs go into `nomira.conf` next to the recipe (or the
file `--config` or `$NOMIRA_CONFIG` names), one `flag = value` per line,
with the long name of the flag:

```ini
# /etc/nomira/nomira.conf
name = laptop
compression = zstd
secret = id=db.key,src=keys/db.key   # relative to this file
secret = id=db.crt,src=/etc/kernel/secureboot/db.crt
pull = always
```

- A flag on the command line wins over the file, and so does its
  environment variable (`NOMIRA_LOG_FORMAT`, `NOMIRA_COLOR`).
- A flag of another command is skipped (`compression` while linting); a
  name that no command knows is an error, so typos show.
- `f`, `config` and `dry-run` cannot be set there.
- The frontend never reads the file. It configures nomira and BuildKit's
  client, nothing that belongs in a recipe.

## Versions

`nomira version` prints the CLI's version; the frontend writes its own
into every image it builds, as the label `org.nomira.frontend.version`,
and into the answer of lint and resolve: the release version (semver)
for releases, `sha-<commit>` for other builds. nomira compares them after
each build. CLI and frontend are
released together, so the same `major.minor` works together; patch
versions do not matter. A different `major.minor` gives a warning, and
so does a version that is not a release (unstable, nothing to compare);
neither stops the build.

## Where the recipe lives

The machine's own recipe lives in `/etc/nomira`, with `recipe.yml` as its
entry file. `/etc` belongs to the machine and keeps local changes across
updates. Every image also carries the recipe it was built from, resolved
with all imports merged, in its label `org.nomira.recipe` (see
`settings.record` in the frontend). On a freshly installed machine,
`sudo nomira recipe export -o /etc/nomira/recipe.yml` makes it the
machine's recipe. Files the recipe copies are not in the label: they are
in the image where they were copied to, and belong in version control
next to the recipe. Without `-f`, every command uses that recipe, so on a nomira
system `sudo nomira build && sudo bootc upgrade` is all it takes. Its build output goes to
`/var/lib/nomira/system.oci`; a recipe elsewhere, for instance while
developing one, gets `.nomira/<name>.oci` next to it.

## What `nomira build` does

1. Find buildkitd, in this order: `BUILDKIT_HOST` if set; a buildkitd
   running locally (first the user's own at
   `$XDG_RUNTIME_DIR/buildkit/buildkitd.sock`, then the system-wide one
   at `/run/buildkit/buildkitd.sock`); otherwise a podman container
   `nomira-buildkitd`, which the CLI creates when needed. Its socket is
   mounted to `$XDG_RUNTIME_DIR/nomira/buildkit/`, so buildctl talks to
   it directly.
2. Build the recipe with `buildctl` into a work directory,
   `.build/<name>.oci` next to the output directory. The conflict check
   of the groups runs in the frontend, and so does every check the
   recipe makes itself, `run bootc container lint` for one: a failing
   step stops the build. BuildKit only writes the layers that are not
   there yet, compressed as `--compression` says (zstd by default), with
   the file times clamped to the recipe's `settings.source_date_epoch`
   (`rewrite-timestamp=true`); layers no longer referenced are removed
   afterwards. A secret that a step requires but no `--secret` gives
   fails that step, and BuildKit says which.

   Every image carries BuildKit's cache keys of its layers in its
   config (inline cache). The build imports them from the last
   published image and from every `--cache-from` image, so a BuildKit
   without cache, after a prune or on another machine, takes every
   unchanged step from there, a package bucket for one, instead of
   running it again. A missing import is skipped with a warning. This
   needs a frontend that passes the imports on (nomira from fd8c2ed on).
3. Read the image's config from the work directory and compare the
   frontend's version with the CLI's.
4. Publish: link the blobs into the output directory and replace its
   `index.json` in one step. The output directory therefore only ever
   holds a complete image of a build that succeeded.

## The recipe of an image

`nomira recipe export` prints the recipe an image was built from, or
writes it to a file with `-o`. `nomira recipe diff A B` compares two
recipes. A source is one of:

- `booted`: the image the running system booted, as `bootc status`
  names it (the default). nomira checks that it is still that very
  image: an OCI directory holds only the newest build, which need not
  be the one that is booted.
- a recipe file (`recipe.yml`): resolved by its frontend through
  BuildKit (`--opt mode=resolve`), so imports are merged as in a build,
  those by URL included;
- an OCI layout directory, such as the output of `nomira build`, which
  nomira reads itself;
- any other image reference, read with skopeo: `docker://…`,
  `containers-storage:…`, `oci-archive:…`.

Without arguments, `nomira recipe diff` compares the booted image with
the recipe `-f` names (by default `/etc/nomira/recipe.yml`): what the
next `nomira build` would change. Both sides are complete recipes, so
imports cannot get in the way. The resolver's notes on which file each
part came from are left out of the comparison; `--origin` keeps them.

## Output

In text mode (the default) every line starts with the time since the
start, and the commands nomira runs stand out. buildctl reports in
rawjson, and nomira shows it one line per event, without redrawing, so
that it reads the same on a terminal and in a log file: a step that is
done, with its time; a step still running after three seconds; the
output of steps, indented below them. Steps from the cache are only
counted, and bookkeeping (resolving images, loading the recipe, the
frontend's checks, diffs and merges) only shows when it fails or takes a
second or more. The downloads of a collector that fans out, one step per
file, are only counted as well (`files fetched: 12 downloaded, 1222 from
the cache`), unless one fails. `--progress plain` or `--progress tty` shows buildctl's
own display instead, between two rules. Colour is used on a terminal
unless `NO_COLOR` is set.

`--log-format json` writes one JSON object per line to stderr, for Loki,
an OpenTelemetry collector or any other log collector: `time`, `level`,
`msg`, `component` (`nomira` or `buildkit`) and the fields of the event.
buildctl then reports in its rawjson format, which nomira turns into
events of the same shape: `step finished` with `step`, `cached` and
`duration_ms` (or `step failed` with `error`), and one event per line of
a step's output with `step` and `stream`. The result, the path of the
published image, stays on stdout as in text mode.

## nomira builds, bootc deploys

nomira has no command that changes the running system. The output
directory is an image source like a registry, and bootc follows it:

```sh
sudo nomira build      # build and publish
sudo bootc upgrade     # take the new image; --apply reboots
```

A system installed with
`--target-transport oci --target-imgref /var/lib/nomira/<name>.oci`
follows the directory from the start. Any other one is pointed at it once
with `sudo bootc switch --transport oci /var/lib/nomira/<name>.oci`.
`bootc status`, `bootc rollback` and the automatic update timer work as
with any other image source.

On a nomira system run `nomira build` as root: it then uses the
system-wide buildkitd. Without root it falls back to the podman
container; this is meant for developing recipes on another machine.

Checks of the image are steps of the recipe, so they run on every build
and are cached like any other step. For a bootc image:

```yaml
build:
  # … the other phases …
  lint:
    bootc: ["run bootc container lint --fatal-warnings"]
```

## Requirements

`buildctl` (Arch package `buildkit`). `podman` only for the managed
buildkitd when no other one answers; `skopeo` only for `recipe` sources
that are neither files nor OCI directories; `bootc` for the source
`booted`.

Environment variables: `NOMIRA_RECIPE` (recipe used without `-f`),
`BUILDKIT_HOST`, `NOMIRA_BUILDKIT_IMAGE` (image
for the managed buildkitd container).

## Building

```sh
go build -o nomira .
go test ./...
```

The CLI has no dependencies beyond the Go standard library.

## Status

The sequence of commands is covered by dry-run tests. `lint`, `build`
and `recipe export`/`diff` have been run against a real BuildKit;
`bootc upgrade` from the published directory is still to be tried on a
real system.

## Releases

Releases are cut by hand: start the "release" workflow in the Actions
tab and choose `beta` or `final`. Version and changelog follow from the
Conventional Commits; details are in
[Nomira-OS/ci](https://github.com/Nomira-OS/ci).

## Licence

EUPL-1.2, see `LICENSES/`. Per-file information is in `REUSE.toml`.
