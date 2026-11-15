package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	buildkitContainer = "nomira-buildkitd"
	// renovate: datasource=docker
	defaultBuildkitImage = "docker.io/moby/buildkit:v0.35.0"
)

// systemSocket is where a system-wide buildkitd listens (Arch's
// buildkit.socket). A variable so tests can point it elsewhere.
var systemSocket = "/run/buildkit/buildkitd.sock"

// nativeSocket returns the socket of a buildkitd running on this machine
// that the current user can connect to: first a rootless one in the user's
// runtime directory, then the system-wide one.
func nativeSocket() string {
	var candidates []string
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "buildkit", "buildkitd.sock"))
	}
	candidates = append(candidates, systemSocket)
	for _, path := range candidates {
		conn, err := net.DialTimeout("unix", path, 2*time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		return path
	}
	return ""
}

// buildkitAddr returns the address buildctl should use, in this order:
// BUILDKIT_HOST if set; a buildkitd already listening on this machine;
// otherwise a buildkitd container managed by podman, started or created
// on demand.
func buildkitAddr(r *runner) (string, error) {
	if host := os.Getenv("BUILDKIT_HOST"); host != "" {
		return host, nil
	}
	if sock := nativeSocket(); sock != "" {
		return "unix://" + sock, nil
	}
	addr := "podman-container://" + buildkitContainer
	image := os.Getenv("NOMIRA_BUILDKIT_IMAGE")
	if image == "" {
		image = defaultBuildkitImage
	}
	create := []string{"run", "-d", "--name", buildkitContainer, "--privileged", image}

	if r.dryRun {
		fmt.Fprintf(r.out, "# start buildkitd container %q, creating it if missing:\n", buildkitContainer)
		r.echo(nil, "podman", create)
		return addr, nil
	}
	state, exists := r.query("podman", "container", "inspect", "-f", "{{.State.Running}}", buildkitContainer)
	switch {
	case !exists:
		return addr, r.run("podman", create...)
	case state != "true":
		return addr, r.run("podman", "start", buildkitContainer)
	}
	return addr, nil
}
