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

// progressSink receives what buildctl's rawjson progress reports.
type progressSink interface {
	started(id, step string)
	done(id, step string, cached bool, d time.Duration, err string)
	output(step, stream, line string)
	warning(step, msg string)
	text(line string) // a line that is not JSON: buildctl's own messages
}

// readProgress parses buildctl's rawjson progress (one SolveStatus per
// line) into calls of sink. Output arrives in chunks; it is passed on
// line by line.
func readProgress(r io.Reader, sink progressSink) {
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
	started := map[string]bool{}
	done := map[string]bool{}
	partial := map[[2]string]string{} // vertex, stream: an unfinished line
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var st status
		if err := json.Unmarshal(sc.Bytes(), &st); err != nil {
			if text := strings.TrimSpace(sc.Text()); text != "" {
				sink.text(text)
			}
			continue
		}
		for _, v := range st.Vertexes {
			if v.Name != "" {
				names[v.Digest] = v.Name
			}
			if v.Started != nil && !v.Cached && v.Completed == nil && !started[v.Digest] {
				started[v.Digest] = true
				sink.started(v.Digest, names[v.Digest])
			}
			if v.Completed == nil || done[v.Digest] {
				continue
			}
			done[v.Digest] = true
			var d time.Duration
			if v.Started != nil {
				d = v.Completed.Sub(*v.Started)
			}
			sink.done(v.Digest, names[v.Digest], v.Cached, d, v.Error)
		}
		for _, lg := range st.Logs {
			stream := "stdout"
			if lg.Stream == 2 {
				stream = "stderr"
			}
			key := [2]string{lg.Vertex, stream}
			text := partial[key] + string(lg.Data)
			lines := strings.Split(text, "\n")
			partial[key] = lines[len(lines)-1]
			for _, line := range lines[:len(lines)-1] {
				sink.output(names[lg.Vertex], stream, strings.TrimRight(line, "\r"))
			}
		}
		for _, w := range st.Warnings {
			sink.warning(names[w.Vertex], string(w.Short))
		}
	}
	for key, rest := range partial {
		if rest != "" {
			sink.output(names[key[0]], key[1], rest)
		}
	}
}

// jsonSink turns progress into log events: one per finished step (with
// its duration, whether it came from the cache, and its error) and one
// per line of output. Lines that are not JSON become events with their
// level.
type jsonSink struct{ l *logger }

func (j jsonSink) started(id, step string) {}
func (j jsonSink) done(id, step string, cached bool, d time.Duration, err string) {
	attrs := []any{"component", "buildkit", "step", step, "cached", cached, "duration_ms", d.Milliseconds()}
	if err != "" {
		j.l.json.Error("step failed", append(attrs, "error", err)...)
		return
	}
	j.l.json.Info("step finished", attrs...)
}
func (j jsonSink) output(step, stream, line string) {
	j.l.json.Info(line, "component", "buildkit", "step", step, "stream", stream)
}
func (j jsonSink) warning(step, msg string) {
	j.l.json.Warn(msg, "component", "buildkit", "step", step)
}
func (j jsonSink) text(line string) {
	j.l.json.Log(context.Background(), textLevel(line), line, "component", "buildkit")
}

func (l *logger) buildkitEvents(r io.Reader) { readProgress(r, jsonSink{l}) }

// textSink is nomira's own view of a build, one line per event and no
// redrawing, so that it reads the same on a terminal and in a log file:
// a step that is done, with its time; a step that is still running after
// a few seconds; the output of steps, indented below. Steps that come
// from the cache are only counted, and BuildKit's and nomira's own
// bookkeeping only shows when it fails or is slow. The downloads of a
// collector that fans out, one step per file and thousands for a
// desktop, are only counted too, unless one fails.
type textSink struct {
	l       *logger
	cached  int
	fetched [2]int // downloaded, from the cache
	mu      sync.Mutex
	running map[string]*time.Timer
}

// stillRunning is how long a step runs before nomira says so.
var stillRunning = 3 * time.Second

func (t *textSink) started(id, step string) {
	if quietStep(step) || fetchStep(step) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running == nil {
		t.running = map[string]*time.Timer{}
	}
	t.running[id] = time.AfterFunc(stillRunning, func() {
		t.l.line("▸", styleDim, t.l.paint(styleDim, step+" …"))
	})
}

func (t *textSink) done(id, step string, cached bool, d time.Duration, err string) {
	t.mu.Lock()
	if tm, ok := t.running[id]; ok {
		tm.Stop()
		delete(t.running, id)
	}
	t.mu.Unlock()
	switch {
	case err != "":
		t.l.line("✗", styleRed, step+"\n"+indent(t.l.paint(styleRed, err)))
	case fetchStep(step) && cached:
		t.fetched[1]++
	case fetchStep(step):
		t.fetched[0]++
	case cached:
		t.cached++
	case quietStep(step) && d < time.Second:
	default:
		t.l.line("✓", styleGreen, step+"  "+t.l.paint(styleDim, seconds(d)))
	}
}

func (t *textSink) output(step, stream, line string) {
	tag := step
	if i := strings.Index(step, "] "); strings.HasPrefix(step, "[") && i > 0 {
		tag = step[:i+1]
	}
	t.l.mu.Lock()
	defer t.l.mu.Unlock()
	fmt.Fprintln(t.l.w, t.l.paint(styleDim, "        │ "+tag+" ")+line)
}

func (t *textSink) warning(step, msg string) { t.l.warn(msg, "step", step) }

func (t *textSink) text(line string) {
	level := textLevel(line)
	line = logrusMessage(line)
	switch level {
	case slog.LevelError:
		t.l.line("✗", styleRed, t.l.paint(styleRed, line))
	case slog.LevelWarn:
		t.l.warn(line)
	default:
		t.l.line("•", styleDim, line)
	}
}

// summary closes the view: how many files were downloaded and how many
// steps came from the cache.
func (t *textSink) summary() {
	if t.fetched[0]+t.fetched[1] > 0 {
		t.l.line("•", styleDim, t.l.paint(styleDim, fmt.Sprintf("files fetched: %d downloaded, %d from the cache", t.fetched[0], t.fetched[1])))
	}
	if t.cached > 0 {
		t.l.line("•", styleDim, t.l.paint(styleDim, fmt.Sprintf("%d steps from the cache", t.cached)))
	}
}

// quietStep is bookkeeping: BuildKit resolving and loading images and
// sources, the frontend's checks, diffs and merges. Shown only when it
// fails or takes a second or more.
func quietStep(step string) bool {
	for _, p := range []string{"resolve image config", "docker-image://", "[load] ", "[base] ", "[check"} {
		if strings.HasPrefix(step, p) {
			return true
		}
	}
	return strings.HasSuffix(step, "] diff") || strings.Contains(step, "] merge ")
}

// fetchStep is a file that a collector that fans out downloads: the
// frontend names those steps "[<phase>/<group>] fetch <file>".
func fetchStep(step string) bool {
	i := strings.Index(step, "] fetch ")
	return strings.HasPrefix(step, "[") && i > 0 && !strings.Contains(step[:i], "]")
}

func indent(s string) string {
	return "        " + strings.ReplaceAll(s, "\n", "\n        ")
}

func seconds(d time.Duration) string {
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return clock(d)
}

func (l *logger) buildkitText(r io.Reader) {
	t := &textSink{l: l}
	readProgress(r, t)
	t.mu.Lock()
	for _, tm := range t.running {
		tm.Stop()
	}
	t.mu.Unlock()
	t.summary()
}

// logrusMessage shortens a logrus line of buildctl (time=… level=…
// msg="…" error="…") to its message and error.
func logrusMessage(line string) string {
	field := func(key string) string {
		i := strings.Index(line, key+`="`)
		if i < 0 {
			return ""
		}
		rest := line[i+len(key)+2:]
		var b strings.Builder
		for j := 0; j < len(rest); j++ {
			switch {
			case rest[j] == '\\' && j+1 < len(rest):
				j++
				b.WriteByte(rest[j])
			case rest[j] == '"':
				return b.String()
			default:
				b.WriteByte(rest[j])
			}
		}
		return b.String()
	}
	msg := field("msg")
	if msg == "" {
		return line
	}
	if e := field("error"); e != "" {
		msg += ": " + e
	}
	return msg
}
