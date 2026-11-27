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
```

Flags (`nomira <command> -h` shows all of them):

| Flag | Meaning |
|---|---|
| `-f recipe.yml` | entry file of the recipe; default `$NOMIRA_RECIPE`, else `/etc/nomira/recipe.yml` |
| `-o DIR` | output directory (see below for the default) |
| `--dry-run` | only print the commands, run nothing |
| `--progress plain` | progress output of buildctl, for a log with every step |
| `--no-boot-test` | skip the bcvk boot test |

No flag changes the image. Everything that does is in the recipe: the
frontend in its first line (`# syntax=…`, required), base image, args,
`settings.compression` for the exported layers, `settings.secrets`
for the files that provide secrets, which the CLI passes to BuildKit,
and `settings.source_date_epoch` for all timestamps.
The same recipe thus builds the same image, whoever runs it.

## Where the recipe lives

The machine's own recipe lives in `/etc/nomira`, with `recipe.yml` as its
entry file. `/etc` belongs to the machine and keeps local changes across
updates. Every image also carries the recipe it was built from,
read-only, in `/usr/share/nomira/recipe` (see `settings.record` in the
frontend); the nomira base image copies it to `/etc/nomira` on first boot
when that does not exist yet. Without `-f`, every command uses that recipe, so on a nomira
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
   `settings.source_date_epoch` (`rewrite-timestamp=true`); layers no
   longer referenced are removed afterwards.
3. Check, if there is anything to check: all `validation.d/*.sh` next to
   the recipe (sorted, with the image name in `$IMG`), then
   `bcvk ephemeral test-basic`, which boots the image in a throwaway VM
   with `systemd.firstboot=off`. Both run the image from the user's
   containers-storage, so for them the image is copied there with
   `skopeo` first. Without hooks and without bcvk nothing is copied.
4. Publish: link the blobs into the output directory and replace its
   `index.json` in one step. The output directory therefore only ever
   holds an image that passed its checks.

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
