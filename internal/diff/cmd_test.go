package diff

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/command"
	"gotest.tools/v3/assert"
)

func setupFakeGH(t *testing.T, script string) string {
	t.Helper()
	binDir := t.TempDir()
	ghPath := filepath.Join(binDir, "gh")
	err := os.WriteFile(ghPath, []byte("#!/bin/sh\n"+script), 0o755)
	assert.NilError(t, err)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	return binDir
}

func TestRunTokenCheckFails(t *testing.T) {
	ws := t.TempDir()
	setupFakeGH(t, `exit 1`)

	err := Run(context.Background(), Options{
		Repo:      "owner/repo",
		PRNumber:  "1",
		Workspace: ws,
		Runner:    &command.ExecRunner{},
	})
	assert.NilError(t, err)

	errData, readErr := os.ReadFile(filepath.Join(ws, artifact.FileError))
	assert.NilError(t, readErr)
	assert.Assert(t, len(errData) > 0, "expected .paco-error to be written")
}

func TestRunDiffTooLarge(t *testing.T) {
	ws := t.TempDir()

	largeFile := filepath.Join(t.TempDir(), "large.txt")
	large := make([]byte, 200001)
	for i := range large {
		large[i] = 'x'
	}
	assert.NilError(t, os.WriteFile(largeFile, large, 0o644))

	setupFakeGH(t, `
case "$1 $2" in
  "api repos/"*) echo '{"id":1}'; exit 0 ;;
  "pr view"*) echo '{"headRefOid":"abc123","baseRefName":"main"}'; exit 0 ;;
  "pr diff"*) cat `+largeFile+`; exit 0 ;;
  *) exit 0 ;;
esac
`)

	err := Run(context.Background(), Options{
		Repo:      "owner/repo",
		PRNumber:  "1",
		Workspace: ws,
		Runner:    &command.ExecRunner{},
	})
	assert.NilError(t, err)

	errData, _ := os.ReadFile(filepath.Join(ws, artifact.FileError))
	assert.Assert(t, len(errData) > 0, "expected .paco-error for oversized diff")
}

func TestRunSuccess(t *testing.T) {
	ws := t.TempDir()
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,3 @@
 package foo
+var x = 1
`
	diffFile := filepath.Join(t.TempDir(), "test.diff")
	assert.NilError(t, os.WriteFile(diffFile, []byte(diff), 0o644))

	setupFakeGH(t, `
case "$1 $2" in
  "api repos/"*) echo '{"id":1}'; exit 0 ;;
  "pr view"*) echo '{"headRefOid":"abc123","baseRefName":"main"}'; exit 0 ;;
  "pr diff"*) cat `+diffFile+`; exit 0 ;;
  *) exit 0 ;;
esac
`)

	err := Run(context.Background(), Options{
		Repo:      "owner/repo",
		PRNumber:  "1",
		Workspace: ws,
		Runner:    &command.ExecRunner{},
	})
	assert.NilError(t, err)

	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileDiff)))
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileValidLines)))
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileHeadSHA)))
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileExistingInline)))
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileExistingFeedback)))

	headSHA, _ := os.ReadFile(filepath.Join(ws, artifact.FileHeadSHA))
	assert.Equal(t, string(headSHA), "abc123")
}

func TestRunRedactsCredentials(t *testing.T) {
	ws := t.TempDir()
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n package a\n+token := \"ghp_ABCDEFghijklmnopqrstuvwx\"\n"
	diffFile := filepath.Join(t.TempDir(), "test.diff")
	assert.NilError(t, os.WriteFile(diffFile, []byte(diff), 0o644))

	setupFakeGH(t, `
case "$1 $2" in
  "api repos/"*) echo '{"id":1}'; exit 0 ;;
  "pr view"*) echo '{"headRefOid":"abc123","baseRefName":"main"}'; exit 0 ;;
  "pr diff"*) cat `+diffFile+`; exit 0 ;;
  *) exit 0 ;;
esac
`)

	err := Run(context.Background(), Options{
		Repo:      "owner/repo",
		PRNumber:  "1",
		Workspace: ws,
		Runner:    &command.ExecRunner{},
	})
	assert.NilError(t, err)

	diffContent, _ := os.ReadFile(filepath.Join(ws, artifact.FileDiff))
	assert.Assert(t, !strings.Contains(string(diffContent), "ghp_ABCDEFghijklmnopqrstuvwx"), "diff should be redacted")
	assert.Assert(t, strings.Contains(string(diffContent), "[REDACTED]"), "diff should contain [REDACTED]")
}

func TestRunToolchains(t *testing.T) {
	diffFile := filepath.Join(t.TempDir(), "test.diff")
	assert.NilError(t, os.WriteFile(diffFile, []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n package a\n+var x = 1\n"), 0o644))

	tests := []struct {
		name       string
		baseBranch string
		root       []string
		files      map[string]string
		listErr    bool
		want       string
		rules      string
	}{
		{
			name: "writes versions of files present at the root",
			root: []string{"README.md", "go.mod", "package.json"},
			files: map[string]string{
				"go.mod":       "module example.com/foo\n\ngo 1.27.1\n",
				"package.json": `{"engines":{"node":">=20"}}`,
			},
			want: "Go\t1.27.1\tgo.mod\nNode.js\t>=20\tpackage.json\n",
		},
		{
			name:       "reads toolchains and rules from a non-main base branch",
			baseBranch: "release/1.2",
			root:       []string{"go.mod"},
			files:      map[string]string{"go.mod": "module example.com/foo\n\ngo 1.26\n"},
			want:       "Go\t1.26\tgo.mod\n",
			rules:      "Review release compatibility\n",
		},
		{
			name: "skips when no version file is present",
			root: []string{"README.md", "Makefile"},
		},
		{
			name:    "skips when listing the root fails",
			listErr: true,
		},
		{
			name: "skips a version file that cannot be fetched",
			root: []string{"go.mod"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			fixtures := t.TempDir()
			workspace := &artifact.Workspace{Dir: ws}
			assert.NilError(t, workspace.Write(artifact.FileToolchains, []byte("Go\t9.99\tgo.mod\n")))
			assert.NilError(t, workspace.Write(artifact.FileReviewRules, []byte("stale rules\n")))
			baseBranch := tt.baseBranch
			if baseBranch == "" {
				baseBranch = "main"
			}
			ref := url.QueryEscape(baseBranch)

			listing := "exit 1"
			if !tt.listErr {
				listFile := filepath.Join(fixtures, "listing")
				assert.NilError(t, os.WriteFile(listFile, []byte(strings.Join(tt.root, "\n")+"\n"), 0o644))
				listing = "cat " + listFile + "; exit 0"
			}
			var fileCases strings.Builder
			for name, content := range tt.files {
				f := filepath.Join(fixtures, name+".b64")
				assert.NilError(t, os.WriteFile(f, []byte(base64.StdEncoding.EncodeToString([]byte(content))+"\n"), 0o644))
				fmt.Fprintf(&fileCases, "  \"api repos/owner/repo/contents/%s?ref=%s\") cat %s; exit 0 ;;\n", name, ref, f)
			}
			if tt.rules != "" {
				f := filepath.Join(fixtures, "rules.b64")
				assert.NilError(t, os.WriteFile(f, []byte(base64.StdEncoding.EncodeToString([]byte(tt.rules))+"\n"), 0o644))
				fmt.Fprintf(&fileCases, "  \"api repos/owner/repo/contents/.tekton/ai/REVIEW.md?ref=%s\") cat %s; exit 0 ;;\n", ref, f)
			}

			setupFakeGH(t, `
case "$1 $2" in
  "api repos/owner/repo/contents?ref=`+ref+`") `+listing+` ;;
`+fileCases.String()+`  "api repos/owner/repo/contents"*) exit 1 ;;
  "api repos/"*) echo '{"id":1}'; exit 0 ;;
  "pr view"*) echo '{"headRefOid":"abc123","baseRefName":"`+baseBranch+`"}'; exit 0 ;;
  "pr diff"*) cat `+diffFile+`; exit 0 ;;
  *) exit 0 ;;
esac
`)

			err := Run(context.Background(), Options{
				Repo:      "owner/repo",
				PRNumber:  "1",
				Workspace: ws,
				Runner:    &command.ExecRunner{},
			})
			assert.NilError(t, err)

			got, readErr := os.ReadFile(filepath.Join(ws, artifact.FileToolchains))
			if tt.want == "" {
				assert.Assert(t, os.IsNotExist(readErr), "expected no %s artifact", artifact.FileToolchains)
			} else {
				assert.NilError(t, readErr)
				assert.Equal(t, tt.want, string(got))
			}
			rules, rulesErr := os.ReadFile(filepath.Join(ws, artifact.FileReviewRules))
			if tt.rules != "" {
				assert.NilError(t, rulesErr)
				assert.Equal(t, tt.rules, string(rules))
			} else {
				assert.Assert(t, os.IsNotExist(rulesErr), "expected no %s artifact", artifact.FileReviewRules)
			}
		})
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
