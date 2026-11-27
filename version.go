package main

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// version of the CLI: the release version (semver, without "v") for
// releases, sha-<short commit> for other builds. Set at build time with
// -ldflags "-X main.version=…"; without it, go install's module version
// or the commit go build records are used.
var version = ""

func cliVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimPrefix(bi.Main.Version, "v"); v != "" && v != "(devel)" && !pseudoVersion.MatchString(v) {
			return v
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return "sha-" + s.Value[:7]
			}
		}
	}
	return "dev"
}

// pseudoVersion matches Go's versions for untagged commits
// (0.0.0-20261127123456-0123456789ab): not a release.
var pseudoVersion = regexp.MustCompile(`-(0\.)?\d{14}-[0-9a-f]{12}(\+[0-9A-Za-z.-]+)?$`)

var semverRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// majorMinor parses a release version. ok is false for anything that is
// not one: sha-…, dev, Go pseudo-versions.
func majorMinor(v string) (major, minor int, ok bool) {
	m := semverRE.FindStringSubmatch(v)
	if m == nil || pseudoVersion.MatchString(v) {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}

// compatibility compares the CLI with the frontend it talks to. CLI and
// frontend are released together: the same major.minor works together,
// patches do not matter. Anything that is not a release cannot be
// compared. The result is a warning, or "" when there is nothing to say;
// never an error, since a mismatch may well work.
func compatibility(cli, frontend string) string {
	if frontend == "" {
		frontend = "unknown (older than its version report)"
	}
	cma, cmi, cok := majorMinor(cli)
	fma, fmi, fok := majorMinor(frontend)
	switch {
	case !cok || !fok:
		return fmt.Sprintf("nomira %s and frontend %s: not both releases, compatibility not checked (unstable versions)", cli, frontend)
	case cma != fma || cmi != fmi:
		return fmt.Sprintf("nomira %s and frontend %s differ in major.minor; they may not work together", cli, frontend)
	}
	return ""
}
