package main

import (
	"fmt"
	"os"
)

const (
	buildkitContainer = "nomira-buildkitd"
	// renovate: datasource=docker
	defaultBuildkitImage = "docker.io/moby/buildkit:v0.35.0"
)

// buildkitAddr returns the address buildctl should use. With BUILDKIT_HOST
// set, that daemon is used as is. Otherwise a buildkitd container managed
// by podman is started or created on demand.
func buildkitAddr(r *runner) (string, error) {
	if host := os.Getenv("BUILDKIT_HOST"); host != "" {
		return host, nil
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
