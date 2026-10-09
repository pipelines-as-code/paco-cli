// Package progress formats public, bounded status lines for terminals and CI logs.
package progress

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pipelines-as-code/paco-cli/internal/security"
)

type Logger struct {
	mu              sync.Mutex
	out             io.Writer
	secrets         []string
	repositoryCalls int64
}

func New(out io.Writer, secrets []string) *Logger {
	if out == nil {
		out = os.Stdout
	}
	return &Logger{out: out, secrets: append([]string(nil), secrets...)}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit - 3
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "..."
}

func (l *Logger) field(s string) string {
	s = security.Scrub(s, l.secrets...)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&b, "\\u%04x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return truncate(b.String(), 256)
}

func (l *Logger) Line(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			args[i] = l.field(s)
		}
	}
	_, _ = fmt.Fprintln(l.out, truncate(fmt.Sprintf(format, args...), 1024))
}

func (l *Logger) RepositoryCalls() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.repositoryCalls
}

func (l *Logger) ToolStart(name string, input json.RawMessage) {
	l.mu.Lock()
	l.repositoryCalls++
	l.mu.Unlock()
	var a struct {
		Path     string `json:"path"`
		Revision string `json:"revision"`
		Start    int    `json:"start_line"`
		End      int    `json:"end_line"`
		Query    string `json:"query"`
		Filter   string `json:"path_contains"`
		Contains string `json:"contains"`
		Hunk     *int   `json:"hunk"`
		Offset   *int   `json:"offset"`
	}
	if len(input) > 4096 || json.Unmarshal(input, &a) != nil {
		l.Line("Calling %s: invalid arguments", name)
		return
	}
	if a.Revision == "" {
		a.Revision = "head"
	}
	switch name {
	case "read_file":
		l.Line("Reading %s:%d-%d [%s]", a.Path, a.Start, a.End, a.Revision)
	case "search_code":
		l.Line("Searching %q [%s] path filter %q", a.Query, a.Revision, a.Filter)
	case "list_files":
		l.Line("Listing files [%s] path filter %q", a.Revision, a.Contains)
	case "read_diff":
		hunk, offset := 1, 1
		if a.Hunk != nil {
			hunk = *a.Hunk
		}
		if a.Offset != nil {
			offset = *a.Offset
		}
		l.Line("Reading diff %s hunk %d offset %d", a.Path, hunk, offset)
	default:
		l.Line("Calling %s", name)
	}
}

var (
	readLine   = regexp.MustCompile(`^[0-9]+: `)
	searchLine = regexp.MustCompile(`^.+:[0-9]+: `)
)

func (l *Logger) ToolEnd(name, output string, err error, elapsed time.Duration) {
	if err != nil {
		l.Line("%s failed (%s): %s", name, elapsed.Round(time.Millisecond).String(), err.Error())
		return
	}
	count := 0
	for _, line := range strings.Split(output, "\n") {
		switch name {
		case "read_file":
			if readLine.MatchString(line) {
				count++
			}
		case "search_code":
			if searchLine.MatchString(line) {
				count++
			}
		case "list_files":
			if line != "" && !strings.Contains(line, "snapshot;") && !strings.HasPrefix(line, "[Result truncated") && line != "No matches in the available snapshot." {
				count++
			}
		case "read_diff":
			if strings.HasPrefix(line, "{") {
				count++
			}
		}
	}
	detail := ""
	if strings.Contains(output, "[Result truncated") {
		detail = "; truncated"
	}
	if name == "search_code" || name == "list_files" {
		if detail == "" {
			detail = "; search complete in available snapshot"
		}
	}
	if name == "read_diff" && strings.Contains(output, "complete: false") {
		detail += "; incomplete hunk context"
	}
	unit := "lines"
	if name == "search_code" {
		unit = "matches"
	}
	if name == "list_files" {
		unit = "files"
	}
	l.Line("%s completed (%s): %d %s returned%s", name, elapsed.Round(time.Millisecond).String(), count, unit, detail)
}

func (l *Logger) WebStart(input json.RawMessage) {
	var a struct {
		Query string `json:"query"`
	}
	if len(input) <= 4096 && json.Unmarshal(input, &a) == nil && a.Query != "" {
		l.Line("Web searching %q", a.Query)
	} else {
		l.Line("Web search started: query unavailable")
	}
}

func (l *Logger) WebEnd(raw string) {
	var result struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil {
		l.Line("Web search completed: result details unavailable; server duration unavailable")
		return
	}
	var hits []json.RawMessage
	if json.Unmarshal(result.Content, &hits) == nil {
		l.Line("Web search completed: %d results returned; server duration unavailable", len(hits))
		return
	}
	var failure struct {
		ErrorCode string `json:"error_code"`
	}
	if json.Unmarshal(result.Content, &failure) == nil && failure.ErrorCode != "" {
		l.Line("Web search failed: %s; server duration unavailable", failure.ErrorCode)
		return
	}
	l.Line("Web search completed: result details unavailable; server duration unavailable")
}
