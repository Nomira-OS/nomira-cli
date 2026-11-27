# nomira-cli

> **Alpha.** Commands, flags and the recipe schema may still change
> without notice. `nomira version` shows what you have.

The command line for [nomira](https://github.com/Nomira-OS/nomira): it
builds a recipe, checks the result and publishes the image to a directory
that bootc follows. Deploying is bootc's: `bootc upgrade` takes the new
image.

The CLI knows the frontend only as a container image. Builds go through
`buildctl`; `lint` and `graph` call the helper modes of the frontend
image with podman. The frontend therefore stays usable without this CLI.

## Commands

```
nomira build    [flags]          build a recipe, check the image, publish it
nomira validate [flags] [image]  run the validation.d hooks and the boot test
nomira lint     [flags]          check a recipe
nomira graph    [flags]          print the pipeline as a Mermaid flowchart
nomira recipe export [-o FILE] [SOURCE]   the recipe an image was built from
nomira recipe diff [--origin] [A [B]]     compare two recipes
```

Flags (`nomira <command> -h` shows all of them):

| Flag | Meaning |
|---|---|
| `-f recipe.yml` | entry file of the recipe; default `$NOMIRA_RECIPE`, else `/etc/nomira/recipe.yml` |
| `-o DIR` | output directory (see below for the default) |
| `--dry-run` | only print the commands, run nothing |
| `--progress plain` | progress output of buildctl in text mode, for a log with every step |
| `--no-boot-test` | skip the bcvk boot test |
| `--log-format json` | output for log collectors, see below; default `text` (`$NOMIRA_LOG_FORMAT`) |
| `--color never` | colour in text output: `auto` (default: on a terminal, unless `NO_COLOR` is set), `always`, `never` (`$NOMIRA_COLOR`) |

No flag changes the image. Everything that does is in the recipe: the
frontend in its first line (`# syntax=…`, required), base image, args,
`settings.compression` for the exported layers, `settings.secrets`
for the files that provide secrets, which the CLI passes to BuildKit,
and `settings.source_date_epoch` for all timestamps.
The same recipe thus builds the same image, whoever runs it.

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
`/var/lib/nomira/<name>.oci`; a recipe elsewhere, for instance while
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
   of the groups runs in the frontend. BuildKit only writes the layers
   that are not there yet, compressed as `settings.compression` says
   (zstd by default), with the file times clamped to
   `settings.source_date_epoch` (`rewrite-timestamp=true`, unless the
   recipe sets it to `false`); layers no longer referenced are removed
   afterwards.
3. Check, if there is anything to check: all `validation.d/*.sh` next to
   the recipe (sorted, with the image name in `$IMG`), then
   `bcvk ephemeral test-basic`, which boots the image in a throwaway VM
   with `systemd.firstboot=off`. Both run the image from the user's
   containers-storage, so for them the image is copied there with
   `skopeo` first. Without hooks and without bcvk nothing is copied.
4. Publish: link the blobs into the output directory and replace its
   `index.json` in one step. The output directory therefore only ever
   holds an image that passed its checks.

## The recipe of an image

`nomira recipe export` prints the recipe an image was built from, or
writes it to a file with `-o`. `nomira recipe diff A B` compares two
recipes. A source is one of:

- `booted`: the image the running system booted, as `bootc status`
  names it (the default). nomira reads it with skopeo and checks that
  it is still that very image: an OCI directory holds only the newest
  build, which need not be the one that is booted.
- a recipe file (`recipe.yml`): resolved by its frontend
  (`nomira-frontend resolve`), so imports are merged as in a build;
- an OCI layout directory, such as the output of `nomira build`;
- any image reference skopeo reads: `docker://…`, `oci:…`,
  `containers-storage:…`.

Without arguments, `nomira recipe diff` compares the booted image with
the recipe `-f` names (by default `/etc/nomira/recipe.yml`): what the
next `nomira build` would change. Both sides are complete recipes, so
imports cannot get in the way. The resolver's notes on which file each
part came from are left out of the comparison; `--origin` keeps them.

## Output

In text mode (the default) every line starts with the time since the
start, the commands nomira runs stand out, and the output of buildctl
sits between two rules. Colour is used on a terminal unless `NO_COLOR`
is set.

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
sudo nomira build      # build, check, publish
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

Example of a hook, `validation.d/10-lint.sh`:

```sh
podman run --rm "$IMG" bootc container lint --fatal-warnings
```

## Requirements

`podman`, `buildctl` (Arch package `buildkit`) and `skopeo`. `bcvk` is optional: if it is missing, the boot
test is skipped with a note. The boot test needs QEMU and requires a
network service and `sshd` to be active in the tested image; otherwise
it runs into a timeout.

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

The sequence of commands is covered by dry-run tests. `lint` and `build`
have been run for real; checks with skopeo and bcvk, and `bootc upgrade`
from the published directory, are still to be tried on a real system.

## Releases

Releases are cut by hand: start the "release" workflow in the Actions
tab and choose `beta` or `final`. Version and changelog follow from the
Conventional Commits; details are in
[Nomira-OS/ci](https://github.com/Nomira-OS/ci).

## Licence

EUPL-1.2, see `LICENSES/`. Per-file information is in `REUSE.toml`.
