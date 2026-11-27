package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// nomira.conf sets flags for repeated runs, so that the image name, the
// secrets or the compression need not be typed every time. Each line is
//
//	flag = value
//
// with the long name of a flag, without dashes; # starts a comment. A
// flag that may be given several times (secret) may appear on several
// lines. A flag on the command line wins over the file, and so does an
// environment variable that stands for it (NOMIRA_LOG_FORMAT, …).
//
// The file is the one --config or NOMIRA_CONFIG names, otherwise
// nomira.conf next to the recipe; a missing nomira.conf is no error.
// Relative paths in it are relative to the file. The file configures the
// CLI only: the frontend never reads it, and nothing in it can be part of
// a recipe, since all of it is an option of the BuildKit client or of
// nomira itself.

const confName = "nomira.conf"

// notInConf are flags that make no sense in the file.
var notInConf = map[string]bool{"f": true, "config": true, "dry-run": true}

// pathFlags are resolved against the file's directory when relative.
var pathFlags = map[string]bool{"o": true}

// envFor names the environment variable that stands for a flag.
var envFor = map[string]string{
	"log-format": "NOMIRA_LOG_FORMAT",
	"color":      "NOMIRA_COLOR",
}

// confEntry is one line of the file.
type confEntry struct {
	key, value string
	line       int
}

// readConf reads a nomira.conf. ok is false when the file does not exist.
func readConf(path string) (entries []confEntry, ok bool, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !found || k == "" {
			return nil, true, fmt.Errorf("%s:%d: expected flag = value", path, n)
		}
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		entries = append(entries, confEntry{k, v, n})
	}
	return entries, true, sc.Err()
}

// applyConf sets the flags of fs from the file at path, except those given
// on the command line or by their environment variable. known holds every
// flag of every command: a key no command knows is an error (a typo), a
// key of another command is skipped.
func applyConf(fs *flag.FlagSet, path string, required bool, known map[string]bool) error {
	entries, ok, err := readConf(path)
	if err != nil {
		return err
	}
	if !ok {
		if required {
			return fmt.Errorf("%s not found", path)
		}
		return nil
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	dir := filepath.Dir(path)
	for _, e := range entries {
		switch {
		case notInConf[e.key]:
			return fmt.Errorf("%s:%d: %s cannot be set in %s", path, e.line, e.key, confName)
		case !known[e.key]:
			return fmt.Errorf("%s:%d: no flag %q", path, e.line, e.key)
		}
		fl := fs.Lookup(e.key)
		if fl == nil || given[e.key] || os.Getenv(envFor[e.key]) != "" && envFor[e.key] != "" {
			continue
		}
		v := e.value
		if pathFlags[e.key] && v != "" && !filepath.IsAbs(v) {
			v = filepath.Join(dir, v)
		}
		if e.key == "secret" {
			v = secretRelativeTo(v, dir)
		}
		if err := fs.Set(e.key, v); err != nil {
			return fmt.Errorf("%s:%d: %s: %w", path, e.line, e.key, err)
		}
	}
	return nil
}

// confPath is the file to read and whether it must exist.
func (o *options) confPath() (string, bool) {
	if o.config != "" {
		return o.config, true
	}
	if c := os.Getenv("NOMIRA_CONFIG"); c != "" {
		return c, true
	}
	return filepath.Join(o.recipeDir(), confName), false
}

// secrets is the repeatable --secret id=ID,src=FILE.
type secrets []string

func (s *secrets) String() string { return strings.Join(*s, " ") }

func (s *secrets) Set(v string) error {
	id, src := parseSecret(v)
	if id == "" || src == "" || strings.ContainsAny(id, "/ ") {
		return fmt.Errorf("%q: expected id=ID,src=FILE", v)
	}
	*s = append(*s, "id="+id+",src="+src)
	return nil
}

func parseSecret(v string) (id, src string) {
	for _, part := range strings.Split(v, ",") {
		k, val, _ := strings.Cut(part, "=")
		switch k {
		case "id":
			id = val
		case "src", "source":
			src = val
		}
	}
	return id, src
}

func secretRelativeTo(v, dir string) string {
	id, src := parseSecret(v)
	if src != "" && !filepath.IsAbs(src) {
		src = filepath.Join(dir, src)
	}
	return "id=" + id + ",src=" + src
}

// ids returns the secret ids given.
func (s secrets) ids() map[string]bool {
	out := map[string]bool{}
	for _, v := range s {
		id, _ := parseSecret(v)
		out[id] = true
	}
	return out
}
