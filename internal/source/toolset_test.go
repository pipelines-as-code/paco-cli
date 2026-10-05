package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestRevisionTools(t *testing.T) {
	tools := &Toolset{
		Head:   &Snapshot{Commit: "head", Files: map[string]string{"new.go": "new\n"}},
		Before: &Snapshot{Commit: "merge", Excluded: 2, Files: map[string]string{"old.go": "old\n"}},
		Diff: &Diff{Files: []FileDiff{{
			OldPath: "old.go", NewPath: "new.go", Status: "renamed",
			Hunks: []Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1, Complete: true, Lines: []DiffLine{
				{Kind: "delete", OldLine: 1, Content: "old"},
				{Kind: "add", NewLine: 1, Content: "new"},
			}}},
		}}},
	}
	assert.Equal(t, len(tools.Definitions()), 4)
	assert.Equal(t, len(tools.Head.Definitions()), 3)
	tests := []struct {
		name, tool, input, want string
		wantErr                 bool
	}{
		{name: "default head", tool: "read_file", input: `{"path":"new.go","start_line":1,"end_line":1}`, want: `1: source_json="new"`},
		{name: "before read", tool: "read_file", input: `{"revision":"before","path":"old.go","start_line":1,"end_line":1}`, want: `1: source_json="old"`},
		{name: "before list", tool: "list_files", input: `{"revision":"before"}`, want: "before snapshot; 2 files excluded"},
		{name: "absent search", tool: "search_code", input: `{"revision":"before","query":"new"}`, want: "No matches in the available snapshot"},
		{name: "no arbitrary ref", tool: "list_files", input: `{"revision":"main"}`, wantErr: true},
		{name: "null revision", tool: "list_files", input: `{"revision":null}`, wantErr: true},
		{name: "unknown argument", tool: "list_files", input: `{"revision":"head","command":"pwd"}`, wantErr: true},
		{name: "diff old side", tool: "read_diff", input: `{"path":"old.go"}`, want: `"old_line":1`},
		{name: "diff new side", tool: "read_diff", input: `{"path":"new.go","offset":2}`, want: `"new_line":1`},
		{name: "diff past end", tool: "read_diff", input: `{"path":"new.go","hunk":2}`, wantErr: true},
		{name: "diff bad offset", tool: "read_diff", input: `{"path":"new.go","offset":0}`, wantErr: true},
		{name: "diff traversal", tool: "read_diff", input: `{"path":"../outside"}`, wantErr: true},
		{name: "null", tool: "list_files", input: `null`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tools.Call(context.Background(), tt.tool, json.RawMessage(tt.input))
			if tt.wantErr {
				assert.Assert(t, err != nil)
				return
			}
			assert.NilError(t, err)
			assert.Assert(t, strings.Contains(result, tt.want), result)
			if strings.Contains(tt.input, `"revision":"before"`) {
				assert.Assert(t, !strings.Contains(result, "PR-head"), result)
				assert.Assert(t, strings.Contains(result, "Missing matches do not prove absence"), result)
			}
		})
	}
}

func TestUnavailableAndBoundedDiffTools(t *testing.T) {
	tools := &Toolset{}
	_, err := tools.Call(context.Background(), "read_file", json.RawMessage(`{"revision":"before"}`))
	assert.ErrorContains(t, err, "before snapshot is unavailable")
	_, err = tools.Call(context.Background(), "read_diff", json.RawMessage(`{"path":"a.go"}`))
	assert.ErrorContains(t, err, "diff is unavailable")
	tools.Diff = &Diff{Files: []FileDiff{{NewPath: "a.go", Status: "added", Hunks: []Hunk{{Lines: []DiffLine{
		{Kind: "add", NewLine: 1, Content: strings.Repeat("x", 20000)},
	}}}}}}
	result, err := tools.Call(context.Background(), "read_diff", json.RawMessage(`{"path":"a.go"}`))
	assert.NilError(t, err)
	assert.Assert(t, len(result) <= 16000)
	assert.Assert(t, strings.Contains(result, "truncated"))
}

func TestReadDiffModeOnly(t *testing.T) {
	tools := &Toolset{Diff: &Diff{Files: []FileDiff{{
		OldPath: "script.sh", NewPath: "script.sh", Status: "modified",
		OldMode: "100755", NewMode: "100644",
	}}}}
	output, err := tools.Call(context.Background(), "read_diff", json.RawMessage(`{"path":"script.sh"}`))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(output, `File modes: old="100755" new="100644"`))
	assert.Assert(t, strings.Contains(output, "No text hunks available."))
}

func TestRevisionSourceQuotes(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "spaces", line: " value := 1  "},
		{name: "tabs", line: "\tvalue := 1\t"},
		{name: "Python indentation", line: "    return value"},
		{name: "string whitespace", line: "value := ` a\t  b `"},
		{name: "escaped string", line: "\tvalue := \"a\\\\b\\\"c\"\r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := &Snapshot{Files: map[string]string{"file": tt.line}}
			tools := &Toolset{Head: snapshot, Before: snapshot}
			for _, revision := range []string{"head", "before"} {
				for _, name := range []string{"read_file", "search_code"} {
					args := map[string]any{"revision": revision}
					if name == "read_file" {
						args["path"], args["start_line"], args["end_line"] = "file", 1, 1
					} else {
						args["query"] = "value"
					}
					input, err := json.Marshal(args)
					assert.NilError(t, err)
					output, err := tools.Call(context.Background(), name, input)
					assert.NilError(t, err)
					_, encoded, ok := strings.Cut(output, "source_json=")
					assert.Assert(t, ok, output)
					var decoded string
					assert.NilError(t, json.Unmarshal([]byte(encoded), &decoded))
					assert.Equal(t, decoded, tt.line)
				}
			}
			legacy, err := snapshot.Call(context.Background(), "read_file", json.RawMessage(`{"path":"file","start_line":1,"end_line":1}`))
			assert.NilError(t, err)
			assert.Equal(t, legacy, "1: "+tt.line+"\n")
		})
	}
}

func TestEncodedSourceResultLimits(t *testing.T) {
	tools := &Toolset{Head: &Snapshot{Files: map[string]string{"file": strings.Repeat("\t", 8000) + "value"}}}
	tests := []struct {
		name  string
		input string
	}{
		{name: "read_file", input: `{"path":"file","start_line":1,"end_line":1}`},
		{name: "search_code", input: `{"query":"value"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := tools.Call(context.Background(), tt.name, json.RawMessage(tt.input))
			assert.NilError(t, err)
			assert.Assert(t, len(output) < 16000)
			assert.Assert(t, strings.Contains(output, "[Result truncated"))
			assert.Assert(t, !strings.Contains(output, "source_json="), "do not emit partial JSON strings")
		})
	}
}

func TestCombinedSnapshotLimits(t *testing.T) {
	tests := []struct {
		name    string
		files   int
		content string
		wantErr string
	}{
		{name: "at retained limit", files: 16, content: strings.Repeat("x", maxFileBytes)},
		{name: "over retained limit", files: 17, content: strings.Repeat("x", maxFileBytes), wantErr: "16 MiB"},
		{name: "over file count", files: 5001, content: "x", wantErr: "10000 files"},
		{name: "encoded aggregate", files: 8, content: strings.Repeat("\x01", maxFileBytes), wantErr: "32 MiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			head := &Snapshot{Commit: "head", Files: map[string]string{}}
			before := &Snapshot{Commit: "before", Files: map[string]string{}}
			for i := range tt.files {
				head.Files[fmt.Sprintf("%d.txt", i)] = tt.content
				before.Files[fmt.Sprintf("%d.txt", i)] = tt.content
			}
			err := ValidateCombined(head, before)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}
