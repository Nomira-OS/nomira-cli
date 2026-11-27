package main

import (
	"bufio"
	"os"
	"strings"
)

// syntaxImage returns the frontend image named by a leading `# syntax=`
// comment, or "" if there is none. Only leading comment lines are
// considered, as in Dockerfiles.
func syntaxImage(recipe string) string {
	f, err := os.Open(recipe)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			return ""
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if k, v, ok := strings.Cut(body, "="); ok && strings.TrimSpace(strings.ToLower(k)) == "syntax" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
