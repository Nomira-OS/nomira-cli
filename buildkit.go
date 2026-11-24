package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	buildkitContainer = "nomira-buildkitd"
	// renovate: datasource=docker
	defaultBuildkitImage = "docker.io/moby/buildkit:v0.35.1"
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

// managedSocketDir is the host directory the managed buildkitd container
// puts its socket into. buildctl then talks to it directly instead of
// through `podman exec`, which is far slower for large images.
func managedSocketDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "nomira", "buildkit")
	}
	if isRoot() {
		return "/run/nomira/buildkit"
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("nomira-%d", os.Getuid()), "buildkit")
}

// listening reports whether something accepts connections on a socket.
func listening(path string) bool {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// buildkitAddr returns the address buildctl should use, in this order:
// BUILDKIT_HOST if set; a buildkitd already listening on this machine;
// otherwise a buildkitd container managed by podman, started or created
// on demand, whose socket is mounted into a directory on the host.
func buildkitAddr(r *runner) (string, error) {
	if host := os.Getenv("BUILDKIT_HOST"); host != "" {
		return host, nil
	}
	if sock := nativeSocket(); sock != "" {
		return "unix://" + sock, nil
	}
	if _, err := os.Stat(systemSocket); err == nil && !isRoot() {
		fmt.Fprintf(os.Stderr, "nomira: %s exists but is not accessible; run nomira as root to use the system buildkitd\n", systemSocket)
	}
	dir := managedSocketDir()
	sock := filepath.Join(dir, "buildkitd.sock")
	addr := "unix://" + sock
	image := os.Getenv("NOMIRA_BUILDKIT_IMAGE")
	if image == "" {
		image = defaultBuildkitImage
	}
	create := []string{"run", "-d", "--name", buildkitContainer, "--privileged",
		"-v", dir + ":/run/buildkit", image}

	if r.dryRun {
		fmt.Fprintf(r.out, "# start buildkitd container %q, creating it if missing:\n", buildkitContainer)
		r.echo(nil, "podman", create)
		return addr, nil
	}
	if listening(sock) {
		return addr, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	mounts, exists := r.query("podman", "container", "inspect", "-f",
		"{{range .Mounts}}{{.Destination}} {{end}}", buildkitContainer)
	if exists && !strings.Contains(mounts, "/run/buildkit") {
		// Created by an older nomira that connected through podman exec.
		if err := r.run("podman", "rm", "-f", buildkitContainer); err != nil {
			return "", err
		}
		exists = false
	}
	var err error
	if exists {
		err = r.run("podman", "start", buildkitContainer)
	} else {
		err = r.run("podman", create...)
	}
	if err != nil {
		return "", err
	}
	for i := 0; i < 60; i++ {
		if listening(sock) {
			return addr, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return "", fmt.Errorf("buildkitd in container %q does not listen on %s; see: podman logs %s",
		buildkitContainer, sock, buildkitContainer)
}
