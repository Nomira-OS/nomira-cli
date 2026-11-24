# nomira-cli

> **Alpha.** Commands, flags and the recipe schema may still change
> without notice. `nomira version` shows what you have.

The command line for [nomira](https://github.com/Nomira-OS/nomira): one
command that builds a recipe, checks the result and switches the running
system to the new image with bootc.

The CLI knows the frontend only as a container image. Builds go through
`buildctl`; `lint` and `graph` call the helper modes of the frontend
image with podman. The frontend therefore stays usable without this CLI.

## Commands

```
nomira build    [flags]          build a recipe into an OCI layout directory
nomira switch   [flags]          build, check, then bootc switch
nomira validate [flags] [image]  run the validation.d hooks and the boot test
nomira lint     [flags]          check a recipe
nomira graph    [flags]          print the pipeline as a Mermaid flowchart
```

Important flags (`nomira <command> -h` shows all of them):

| Flag | Meaning |
|---|---|
| `-f recipe.yml` | entry file of the recipe; default `$NOMIRA_RECIPE`, else `/etc/nomira/recipe.yml` |
| `--dry-run` | only print the commands, run nothing |
| `--build-arg NAME=VALUE` | override an arg declared in `settings.args` |
| `--frontend IMAGE` | frontend image; otherwise the `# syntax=` line of the recipe, otherwise `ghcr.io/nomira-os/nomira:1-alpha` |
| `--name NAME` | image name; otherwise `settings.name`, otherwise the directory name |
| `--no-boot-test` | skip the bcvk boot test |
| `--no-uki` | build no unified kernel image (frontend option `uki=false`) |
| `--apply` | reboot into the new image right after `switch` |

## Where the recipe lives

The machine's own recipe lives in `/etc/nomira`, with `recipe.yml` as its
entry file. `/etc` belongs to the machine and keeps local changes across
updates. Without `-f`, every command uses that recipe, so on a nomira
system `sudo nomira switch` is all it takes. Its build output goes to
`/var/lib/nomira/<name>.oci`; a recipe elsewhere, for instance while
developing one, gets `.nomira/<name>.oci` next to it.

## What `nomira switch` does

1. Find buildkitd, in this order: `BUILDKIT_HOST` if set; a buildkitd
   running locally (first the user's own at
   `$XDG_RUNTIME_DIR/buildkit/buildkitd.sock`, then the system-wide one
   at `/run/buildkit/buildkitd.sock`); otherwise a podman container
   `nomira-buildkitd`, which the CLI creates when needed. Its socket is
   mounted to `$XDG_RUNTIME_DIR/nomira/buildkit/`, so buildctl talks to
   it directly.
2. Build the recipe with `buildctl`. The conflict check of the groups
   runs in the frontend. The result is an OCI layout directory (see
   above for where). BuildKit only writes the
   layers that are not there yet, compressed with zstd; layers no longer
   referenced are removed afterwards.
   For a bootc base image the frontend ends the build with a unified
   kernel image (UKI) in `/boot/EFI/Linux`; `--no-uki` turns it off.
3. Check, if there is anything to check: all `validation.d/*.sh` next to
   the recipe (sorted, with the image name in `$IMG`), then
   `bcvk ephemeral test-basic`, which boots the image in a throwaway VM
   with `systemd.firstboot=off`. Both run the image from the user's
   containers-storage, so for them the image is copied there with
   `skopeo` first. Without hooks and without bcvk nothing is copied.
4. Only when everything has passed: `bootc switch --transport oci` with
   the directory. bootc reads it directly and imports only the layers it
   does not have yet.

On a nomira system the whole thing runs as root: `sudo nomira switch`.
The CLI then uses the system-wide buildkitd and needs no `sudo` for
individual steps. Without root it falls back to the podman container; this is
meant for developing recipes on another machine.

Nothing on the running system changes before step 4.
`nomira switch --dry-run` shows the exact sequence of commands.

Example of a hook, `validation.d/10-lint.sh`:

```sh
podman run --rm "$IMG" bootc container lint --fatal-warnings
```

## Requirements

`podman`, `buildctl` (Arch package `buildkit`) and `skopeo`. For `switch`
also `bootc` and `sudo`. `bcvk` is optional: if it is missing, the boot
test is skipped with a note. The boot test needs QEMU and requires a
network service and `sshd` to be active in the tested image; otherwise
it runs into a timeout.

Environment variables: `NOMIRA_RECIPE` (recipe used without `-f`),
`BUILDKIT_HOST`, `NOMIRA_BUILDKIT_IMAGE` (image
for the managed buildkitd container), `NOMIRA_SUDO` (command used to gain
root).

## Building

```sh
go build -o nomira .
go test ./...
```

The CLI has no dependencies beyond the Go standard library.

## Status

The sequence of commands is covered by dry-run tests. `lint` and `build`
have been run for real; a real run of `switch` against skopeo, bcvk and
bootc is still outstanding.

## Releases

Releases are cut by hand: start the "release" workflow in the Actions
tab and choose `beta` or `final`. Version and changelog follow from the
Conventional Commits; details are in
[Nomira-OS/ci](https://github.com/Nomira-OS/ci).

## Licence

EUPL-1.2, see `LICENSES/`. Per-file information is in `REUSE.toml`.
