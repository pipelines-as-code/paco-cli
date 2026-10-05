package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"gotest.tools/v3/assert"
)

func TestRunSourceTools(t *testing.T) {
	tests := []struct {
		name        string
		snapshot    string
		disabled    bool
		wantTools   bool
		wantFailure bool
	}{
		{name: "snapshot enables tools", snapshot: `{"commit":"sha","files":{"a.go":"package a"}}`, wantTools: true},
		{name: "opt out", snapshot: `{"commit":"sha","files":{"a.go":"package a"}}`, disabled: true},
		{name: "no snapshot remains diff only"},
		{name: "wrong commit fails", snapshot: `{"commit":"other","files":{}}`, wantFailure: true},
		{name: "corrupt snapshot fails", snapshot: `{`, wantFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := setupWorkspaceWithDiff(t, "diff")
			assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileHeadSHA), []byte("sha\n"), 0o600))
			if tt.snapshot != "" {
				assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileSource), []byte(tt.snapshot), 0o600))
			}
			fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
			assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: fakeResolve(fc), NoExploration: tt.disabled}))
			assert.Equal(t, fileExists(filepath.Join(ws, artifact.FileFailed)), tt.wantFailure)
			if tt.wantFailure {
				assert.Equal(t, fc.calls, 0)
				return
			}
			assert.Equal(t, fc.got.Tools != nil, tt.wantTools)
			if tt.wantTools {
				assert.Equal(t, len(fc.got.Tools.Definitions()), 3)
				assert.Assert(t, strings.Contains(fc.got.System, "untrusted DATA"))
			}
		})
	}
}

func TestSourceCannotFollowExternalSymlink(t *testing.T) {
	ws := &artifact.Workspace{Dir: t.TempDir()}
	target := filepath.Join(t.TempDir(), "outside.json")
	assert.NilError(t, os.WriteFile(target, []byte(`{"commit":"sha","files":{}}`), 0o600))
	assert.NilError(t, os.Symlink(target, ws.Path(artifact.FileSource)))
	_, err := loadSource(ws, nil)
	assert.Assert(t, err != nil)
}

func TestWebSearchRequiresExplicitPlainOutput(t *testing.T) {
	for _, plain := range []bool{false, true} {
		ws := setupWorkspaceWithDiff(t, "diff")
		fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
		assert.NilError(t, Run(context.Background(), Options{
			Workspace: ws, Resolve: fakeResolve(fc), WebSearch: true, NoStructuredOutput: plain,
		}))
		assert.Equal(t, fileExists(filepath.Join(ws, artifact.FileFailed)), !plain)
		if plain {
			assert.Assert(t, fc.got.WebSearch)
			assert.Assert(t, fc.got.Schema == nil)
			assert.Assert(t, strings.Contains(fc.got.System, "public library documentation"))
		} else {
			assert.Equal(t, fc.calls, 0)
		}
	}
}

func TestCommandToolDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		web         bool
		exploration bool
		schema      bool
		failed      bool
	}{
		{name: "tools on by default", web: true, exploration: true},
		{name: "disable web only", args: []string{"--web-search=false"}, exploration: true},
		{name: "disable repository only", args: []string{"--no-exploration"}, web: true},
		{name: "disable both", args: []string{"--web-search=false", "--no-exploration"}},
		{name: "structured output opt in", args: []string{"--web-search=false", "--no-structured-output=false"}, exploration: true, schema: true},
		{name: "incompatible settings", args: []string{"--no-structured-output=false"}, failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := setupWorkspaceWithDiff(t, "diff")
			assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileHeadSHA), []byte("sha"), 0o600))
			assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileSource),
				[]byte(`{"commit":"sha","files":{"a.go":"package a"}}`), 0o600))
			fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
			cmd := newCommand(Options{Resolve: fakeResolve(fc)})
			cmd.SetArgs(append([]string{"--workspace", ws}, tt.args...))
			assert.NilError(t, cmd.Execute())
			assert.Equal(t, fileExists(filepath.Join(ws, artifact.FileFailed)), tt.failed)
			if tt.failed {
				assert.Equal(t, fc.calls, 0)
				return
			}
			assert.Equal(t, fc.calls, 1)
			assert.Equal(t, fc.got.WebSearch, tt.web)
			assert.Equal(t, fc.got.Tools != nil, tt.exploration)
			assert.Equal(t, fc.got.Schema != nil, tt.schema)
		})
	}
}
