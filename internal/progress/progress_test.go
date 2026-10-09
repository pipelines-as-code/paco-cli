package progress

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gotest.tools/v3/assert"
)

func TestSafeLines(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, []string{"literal-secret"})
	log.Line("Status: %s", "literal-secret\n\x1b[31m\u202e"+strings.Repeat("é", 200)+"ghp_abcdefghijklmnopqrstuv")
	s := out.String()
	assert.Equal(t, strings.Count(s, "\n"), 1)
	assert.Assert(t, strings.Contains(s, `[REDACTED]\u000a\u001b[31m\u202e`), s)
	assert.Assert(t, !strings.Contains(s, "literal-secret") && !strings.Contains(s, "ghp_"))
	assert.Assert(t, len(s) <= 1025 && utf8.ValidString(s))
	log.Line("%s%s%s%s%s", strings.Repeat("a", 300), strings.Repeat("b", 300), strings.Repeat("c", 300), strings.Repeat("d", 300), strings.Repeat("e", 300))
	assert.Assert(t, len(strings.Split(out.String(), "\n")[1]) <= 1024)
}

func TestToolLogging(t *testing.T) {
	tests := []struct {
		name, tool, input, output, start, end string
		err                                   error
	}{
		{name: "read", tool: "read_file", input: `{"path":"a.go","revision":"before","start_line":3,"end_line":5}`, output: "before snapshot; context\n3: source_json=\"private source\"\n4: source_json=\"more\"\n", start: "Reading a.go:3-5 [before]", end: "2 lines returned"},
		{name: "search", tool: "search_code", input: `{"query":"secret","path_contains":"pkg"}`, output: "head snapshot; context\npkg/a.go:3: source_json=\"private source\"\n", start: `Searching "secret" [head] path filter "pkg"`, end: "1 matches returned; search complete in available snapshot"},
		{name: "truncated search", tool: "search_code", input: `{"query":"q"}`, output: "a.go:1: source\n[Result truncated; narrow the search.]\n", start: `Searching "q"`, end: "1 matches returned; truncated"},
		{name: "empty search", tool: "search_code", input: `{"query":"q"}`, output: "PR-head snapshot; context\nNo matches in the available snapshot.\n", start: `Searching "q"`, end: "0 matches returned; search complete in available snapshot"},
		{name: "file listing", tool: "list_files", input: `{"contains":"pkg"}`, output: "head snapshot; context\npkg/a.go\npkg/b.go\n", start: `Listing files [head] path filter "pkg"`, end: "2 files returned"},
		{name: "diff defaults", tool: "read_diff", input: `{"path":"a.go"}`, output: "Hunk 1/1; complete: false\n{\"content\":\"private source\"}\n", start: "Reading diff a.go hunk 1 offset 1", end: "1 lines returned; incomplete hunk context"},
		{name: "diff slice", tool: "read_diff", input: `{"path":"a.go","hunk":2,"offset":8}`, output: "", start: "Reading diff a.go hunk 2 offset 8", end: "0 lines returned"},
		{name: "malformed", tool: "read_file", input: `{`, start: "Calling read_file: invalid arguments", end: "failed", err: errors.New("bad\ninput")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			log := New(&out, nil)
			log.ToolStart(tt.tool, json.RawMessage(tt.input))
			log.ToolEnd(tt.tool, tt.output, tt.err, time.Millisecond)
			assert.Assert(t, strings.Contains(out.String(), tt.start), out.String())
			assert.Assert(t, strings.Contains(out.String(), tt.end), out.String())
			assert.Assert(t, !strings.Contains(out.String(), "private source"))
			assert.Equal(t, strings.Count(out.String(), "\n"), 2)
			assert.Equal(t, log.RepositoryCalls(), int64(1))
		})
	}
}

func TestWebLogging(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, nil)
	log.WebStart(json.RawMessage(`{"query":"public API"}`))
	log.WebEnd(`{"content":[{"url":"https://example.com","encrypted_content":"never print"}]}`)
	log.WebEnd(`{"content":{"type":"web_search_tool_result_error","error_code":"unavailable"}}`)
	assert.Assert(t, strings.Contains(out.String(), `Web searching "public API"`))
	assert.Assert(t, strings.Contains(out.String(), "1 results returned; server duration unavailable"))
	assert.Assert(t, strings.Contains(out.String(), "Web search failed: unavailable"))
	assert.Assert(t, !strings.Contains(out.String(), "never print") && !strings.Contains(out.String(), "example.com"))
}
