package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/security"
	"gotest.tools/v3/assert"
)

type archiveFile struct {
	name    string
	content string
	kind    byte
}

func archive(t *testing.T, files ...archiveFile) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		kind := f.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: f.name, Mode: 0o600, Typeflag: kind}
		if kind == tar.TypeXGlobalHeader {
			h = &tar.Header{Typeflag: kind, PAXRecords: map[string]string{"comment": f.content}}
		}
		if kind == tar.TypeReg {
			h.Size = int64(len(f.content))
		}
		assert.NilError(t, tw.WriteHeader(h))
		if kind == tar.TypeReg {
			_, err := tw.Write([]byte(f.content))
			assert.NilError(t, err)
		}
	}
	assert.NilError(t, tw.Close())
	assert.NilError(t, gz.Close())
	return data.Bytes()
}

func TestSnapshotFiltersAndRedacts(t *testing.T) {
	data := archive(
		t,
		archiveFile{name: "root/a.go", content: "package a\n// secret-literal\n"},
		archiveFile{name: "root/keys.txt", content: "ghp_ABCDEFghijklmnopqrstuvwx"},
		archiveFile{name: "root/.envrc", content: "secret"},
		archiveFile{name: "root/sub/credentials.json", content: "secret"},
		archiveFile{name: "root/vendor/dep.go", content: "vendored"},
		archiveFile{name: "root/link", kind: tar.TypeSymlink},
		archiveFile{name: "root/hardlink", kind: tar.TypeLink},
		archiveFile{name: "root/image.bin", content: "\x00\xff"},
		archiveFile{name: "root/big.txt", content: strings.Repeat("a", maxFileBytes+1)},
	)
	s, err := FromArchive(data, "sha", "secret-literal")
	assert.NilError(t, err)
	assert.Equal(t, s.Commit, "sha")
	assert.Equal(t, len(s.Files), 2)
	assert.Equal(t, s.Excluded, 7)
	assert.Equal(t, s.Files["a.go"], "package a\n// [REDACTED]\n")
	assert.Equal(t, s.Files["keys.txt"], "[REDACTED]")
	encoded, err := json.Marshal(s)
	assert.NilError(t, err)
	decoded, err := Decode(encoded, "sha")
	assert.NilError(t, err)
	assert.DeepEqual(t, decoded, s)
}

func TestSnapshotSkipsPAXGlobalHeader(t *testing.T) {
	data := archive(
		t,
		archiveFile{kind: tar.TypeXGlobalHeader, content: "0123456789abcdef"},
		archiveFile{name: "root/a.go", content: "package a\n"},
	)
	s, err := FromArchive(data, "sha")
	assert.NilError(t, err)
	assert.DeepEqual(t, s.Files, map[string]string{"a.go": "package a\n"})
	assert.Equal(t, s.Excluded, 0)
}

func TestSnapshotRejectsUnsafeArchives(t *testing.T) {
	tests := []struct {
		name  string
		files []archiveFile
	}{
		{name: "traversal", files: []archiveFile{{name: "root/../escape"}}},
		{name: "absolute", files: []archiveFile{{name: "/root/escape"}}},
		{name: "backslash", files: []archiveFile{{name: `root/sub\escape`}}},
		{name: "no root", files: []archiveFile{{name: "file.go"}}},
		{name: "duplicate", files: []archiveFile{{name: "root/a"}, {name: "root/a"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromArchive(archive(t, tt.files...), "sha")
			assert.Assert(t, err != nil)
		})
	}
}

func TestSnapshotExcludesPrivateKeyContent(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\nsynthetic-key-body\n-----END RSA PRIVATE KEY-----\n"
	serviceAccount, err := json.Marshal(map[string]string{"private_key": key})
	assert.NilError(t, err)
	files := map[string]string{"id_rsa": key, "gcp.json": string(serviceAccount)}
	var entries []archiveFile
	dir := t.TempDir()
	for name, content := range files {
		entries = append(entries, archiveFile{name: "root/" + name, content: content})
		assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	archived, err := FromArchive(archive(t, entries...), "sha")
	assert.NilError(t, err)
	local, err := FromDir(dir, "sha")
	assert.NilError(t, err)
	for _, snapshot := range []*Snapshot{archived, local} {
		assert.Equal(t, len(snapshot.Files), 0)
		assert.Equal(t, snapshot.Excluded, len(files))
		_, err := snapshot.Call(context.Background(), "read_file", json.RawMessage(`{"path":"id_rsa","start_line":1,"end_line":3}`))
		assert.ErrorContains(t, err, "file is not available")
	}
	for name, content := range files {
		for _, text := range []string{content, security.Scrub(content)} {
			data, err := json.Marshal(&Snapshot{Commit: "sha", Files: map[string]string{name: text}})
			assert.NilError(t, err)
			_, err = Decode(data, "sha")
			assert.ErrorContains(t, err, "invalid file")
		}
	}
}

func TestDecodeSnapshotRejectsInvalidData(t *testing.T) {
	tests := []struct{ name, data, commit string }{
		{name: "invalid json", data: "{", commit: "sha"},
		{name: "wrong commit", data: `{"commit":"other","files":{}}`, commit: "sha"},
		{name: "missing commit", data: `{"files":{}}`},
		{name: "traversal", data: `{"commit":"sha","files":{"../x":"x"}}`, commit: "sha"},
		{name: "credential file", data: `{"commit":"sha","files":{".env":"x"}}`, commit: "sha"},
		{name: "binary", data: `{"commit":"sha","files":{"x":"\u0000"}}`, commit: "sha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode([]byte(tt.data), tt.commit)
			assert.Assert(t, err != nil)
		})
	}
}

func TestDecodeBoundsRedactedContent(t *testing.T) {
	data, err := json.Marshal(&Snapshot{Commit: "sha", Files: map[string]string{"a": strings.Repeat("x", maxFileBytes)}})
	assert.NilError(t, err)
	_, err = Decode(data, "sha", "x")
	assert.ErrorContains(t, err, "redacted source file exceeds")
}

func TestSourceLimits(t *testing.T) {
	_, err := Decode(make([]byte, MaxSnapshotBytes+1), "sha")
	assert.ErrorContains(t, err, "exceeds")
	_, err = FromArchive(make([]byte, MaxArchiveBytes+1), "sha")
	assert.ErrorContains(t, err, "exceeds")
	_, err = Decode([]byte(`{"commit":"sha","files":{"a":"`+strings.Repeat("a", maxFileBytes+1)+`"}}`), "sha")
	assert.ErrorContains(t, err, "invalid file")
}

func TestReadOnlyTools(t *testing.T) {
	s := &Snapshot{Commit: "sha", Excluded: 2, Files: map[string]string{
		"a.go":          "package a\nfunc Caller() { Helper() }\n",
		"sub/helper.go": "package a\nfunc Helper() {}\n",
	}}
	tests := []struct {
		name, tool, input, want string
		wantErr                 bool
	}{
		{name: "list", tool: "list_files", input: `{"contains":"helper"}`, want: "sub/helper.go\n"},
		{name: "read", tool: "read_file", input: `{"path":"a.go","start_line":2,"end_line":2}`, want: "2: func Caller() { Helper() }\n"},
		{name: "search", tool: "search_code", input: `{"query":"Helper","path_contains":"sub/"}`, want: "sub/helper.go:2: func Helper() {}"},
		{name: "missing", tool: "read_file", input: `{"path":"missing","start_line":1,"end_line":2}`, wantErr: true},
		{name: "traversal", tool: "read_file", input: `{"path":"../.envrc","start_line":1,"end_line":2}`, wantErr: true},
		{name: "absolute", tool: "read_file", input: `{"path":"/etc/passwd","start_line":1,"end_line":2}`, wantErr: true},
		{name: "large range", tool: "read_file", input: `{"path":"a.go","start_line":1,"end_line":201}`, wantErr: true},
		{name: "zero line", tool: "read_file", input: `{"path":"a.go","start_line":0,"end_line":2}`, wantErr: true},
		{name: "invalid JSON", tool: "list_files", input: `{`, wantErr: true},
		{name: "unknown field", tool: "list_files", input: `{"command":"pwd"}`, wantErr: true},
		{name: "multiple inputs", tool: "list_files", input: `{} {}`, wantErr: true},
		{name: "empty query", tool: "search_code", input: `{"query":""}`, wantErr: true},
		{name: "no execution", tool: "bash", input: `{}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Call(context.Background(), tt.tool, json.RawMessage(tt.input))
			if tt.wantErr {
				assert.Assert(t, err != nil)
				return
			}
			assert.NilError(t, err)
			assert.Assert(t, strings.Contains(got, tt.want), got)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Call(ctx, "list_files", json.RawMessage(`{}`))
	assert.Equal(t, err, context.Canceled)
}

func TestToolResultsAreBounded(t *testing.T) {
	s := &Snapshot{Files: map[string]string{"a.go": strings.Repeat("match "+strings.Repeat("a", 400)+"\n", 200)}}
	for _, tool := range []string{"read_file", "search_code"} {
		input := `{"path":"a.go","start_line":1,"end_line":200}`
		if tool == "search_code" {
			input = `{"query":"match"}`
		}
		result, err := s.Call(context.Background(), tool, json.RawMessage(input))
		assert.NilError(t, err)
		assert.Assert(t, len(result) <= 16000)
		assert.Assert(t, strings.Contains(result, "truncated"))
	}
}
