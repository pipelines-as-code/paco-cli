package diff

import (
	"context"
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
  "pr view"*) echo "abc123"; exit 0 ;;
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
  "pr view"*) echo "abc123"; exit 0 ;;
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
  "pr view"*) echo "abc123"; exit 0 ;;
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
