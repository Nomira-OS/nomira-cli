package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Output of the CLI.
//
// Text (the default) is for people: every line starts with the time since
// the start, the commands nomira runs stand out, and the output of
// buildctl sits between two rules. Colour only on a terminal, or as
// --color says; NO_COLOR turns it off.
//
// JSON (--log-format json) is for log collectors such as Loki or an
// OpenTelemetry collector: one object per line on stderr with time,
// level and msg, and the fields of the event. buildctl then reports in
// rawjson, which nomira turns into events of the same shape, one per
// finished step and one per line of a step's output.

const (
	formatText = "text"
	formatJSON = "json"
)

type logger struct {
	format    string
	color     bool
	colorMode string
	w         io.Writer
	start     time.Time // zero in tests: no times in text lines
	json      *slog.Logger
	mu        sync.Mutex
}

func newLogger(w io.Writer, format, color string, start time.Time) (*logger, error) {
	l := &logger{format: format, w: w, start: start, colorMode: color}
	switch format {
	case "", formatText:
		l.format = formatText
	case formatJSON:
		l.json = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey && start.IsZero() {
					return slog.Attr{} // stable output in tests
				}
				return a
			},
		}))
	default:
		return nil, fmt.Errorf("--log-format: %q is not text or json", format)
	}
	switch color {
	case "", "auto":
		l.color = os.Getenv("NO_COLOR") == "" && isTerminal(w)
	case "always":
		l.color = true
	case "never":
	default:
		return nil, fmt.Errorf("--color: %q is not auto, always or never", color)
	}
	return l, nil
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminalFile(f)
}

// colorOn reports whether output to w gets colour: the same rule as the
// log, for another writer (the diff goes to stdout).
func (l *logger) colorOn(w io.Writer) bool {
	switch l.colorMode {
	case "always":
		return true
	case "never":
		return false
	}
	return os.Getenv("NO_COLOR") == "" && isTerminal(w)
}

// ANSI styles.
const (
	styleDim    = "2"
	styleBold   = "1"
	styleCyan   = "36"
	styleGreen  = "32"
	styleYellow = "33"
	styleRed    = "31"
)

func (l *logger) paint(style, s string) string {
	if !l.color || s == "" {
		return s
	}
	return "\x1b[" + style + "m" + s + "\x1b[0m"
}

func (l *logger) line(mark, markStyle, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	prefix := ""
	if !l.start.IsZero() {
		prefix = l.paint(styleDim, fmt.Sprintf("%5s", clock(time.Since(l.start)))) + "  "
	}
	fmt.Fprintln(l.w, prefix+l.paint(markStyle, mark)+" "+text)
}

// command: nomira runs a command.
func (l *logger) command(env []string, name string, args []string) {
	if l.format == formatJSON {
		l.json.Info("command started", "component", "nomira", "cmd", name, "args", shellJoin(args), "env", shellJoin(env))
		return
	}
	text := l.paint(styleBold, name)
	if len(env) > 0 {
		text = shellJoin(env) + " " + text
	}
	if len(args) > 0 {
		text += " " + shellJoin(args)
	}
	l.line("+", styleCyan, text)
}

// finished: a command nomira ran is done.
func (l *logger) finished(name string, d time.Duration, err error) {
	if l.format == formatJSON {
		if err != nil {
			l.json.Error("command failed", "component", "nomira", "cmd", name, "duration_ms", d.Milliseconds(), "error", err.Error())
		} else {
			l.json.Info("command finished", "component", "nomira", "cmd", name, "duration_ms", d.Milliseconds())
		}
		return
	}
	if l.start.IsZero() {
		return
	}
	if err != nil {
		l.line("✗", styleRed, fmt.Sprintf("%s failed after %s", name, clock(d)))
		return
	}
	l.line("✓", styleGreen, fmt.Sprintf("%s %s", name, l.paint(styleDim, clock(d))))
}

// info and warn report what nomira itself does or skips.
func (l *logger) info(msg string, kv ...any) {
	if l.format == formatJSON {
		l.json.Info(msg, append([]any{"component", "nomira"}, kv...)...)
		return
	}
	l.line("•", styleCyan, msg+kvText(kv))
}

func (l *logger) warn(msg string, kv ...any) {
	if l.format == formatJSON {
		l.json.Warn(msg, append([]any{"component", "nomira"}, kv...)...)
		return
	}
	l.line("!", styleYellow, l.paint(styleYellow, msg)+kvText(kv))
}

// fail reports the error that ends nomira; in text mode always on stderr,
// where it belongs even when a dry run prints its commands to stdout.
func (l *logger) fail(stderr io.Writer, err error) {
	if l.format == formatJSON {
		l.json.Error("nomira failed", "component", "nomira", "error", err.Error())
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(stderr, l.paint(styleRed, "nomira: "+err.Error()))
}

// rule frames the output of a tool in text mode.
func (l *logger) rule(title string) {
	if l.format == formatJSON {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := ""
	if title != "" {
		t = "── " + title + " "
	}
	fmt.Fprintln(l.w, l.paint(styleDim, t+strings.Repeat("─", max(0, 72-len([]rune(t))))))
}

func kvText(kv []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, "  %v=%v", kv[i], kv[i+1])
	}
	return b.String()
}

func shellJoin(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'$") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// textLevel guesses the level of a line buildctl wrote as text: its
// logrus lines say it (level=warning), its final error starts with
// "error:".
func textLevel(text string) slog.Level {
	switch {
	case strings.HasPrefix(text, "error:"), strings.Contains(text, "level=error"), strings.Contains(text, "level=fatal"):
		return slog.LevelError
	case strings.Contains(text, "level=warning"):
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

// buildkitEvents reads buildctl's rawjson progress and logs it as events:
// one per finished step (with its duration, whether it came from the
// cache, and its error) and one per line of output. Lines that are not
// JSON, buildctl's own messages, become events with their level.
func (l *logger) buildkitEvents(r io.Reader) {
	type vertex struct {
		Digest    string     `json:"digest"`
		Name      string     `json:"name"`
		Started   *time.Time `json:"started"`
		Completed *time.Time `json:"completed"`
		Cached    bool       `json:"cached"`
		Error     string     `json:"error"`
	}
	type status struct {
		Vertexes []vertex `json:"vertexes"`
		Logs     []struct {
			Vertex string `json:"vertex"`
			Stream int    `json:"stream"`
			Data   []byte `json:"data"`
		} `json:"logs"`
		Warnings []struct {
			Vertex string `json:"vertex"`
			Short  []byte `json:"short"`
		} `json:"warnings"`
	}
	names := map[string]string{}
	done := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	ctx := context.Background()
	for sc.Scan() {
		var st status
		if err := json.Unmarshal(sc.Bytes(), &st); err != nil {
			if text := strings.TrimSpace(sc.Text()); text != "" {
				l.json.Log(ctx, textLevel(text), text, "component", "buildkit")
			}
			continue
		}
		for _, v := range st.Vertexes {
			if v.Name != "" {
				names[v.Digest] = v.Name
			}
			if v.Completed == nil || done[v.Digest] {
				continue
			}
			done[v.Digest] = true
			attrs := []any{"component", "buildkit", "step", names[v.Digest], "cached", v.Cached}
			if v.Started != nil {
				attrs = append(attrs, "duration_ms", v.Completed.Sub(*v.Started).Milliseconds())
			}
			if v.Error != "" {
				l.json.ErrorContext(ctx, "step failed", append(attrs, "error", v.Error)...)
			} else {
				l.json.InfoContext(ctx, "step finished", attrs...)
			}
		}
		for _, lg := range st.Logs {
			stream := "stdout"
			if lg.Stream == 2 {
				stream = "stderr"
			}
			for _, text := range strings.Split(strings.TrimRight(string(lg.Data), "\n"), "\n") {
				l.json.InfoContext(ctx, text, "component", "buildkit", "step", names[lg.Vertex], "stream", stream)
			}
		}
		for _, w := range st.Warnings {
			l.json.WarnContext(ctx, string(w.Short), "component", "buildkit", "step", names[w.Vertex])
		}
	}
}
