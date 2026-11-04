package source_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"gotest.tools/v3/assert"
)

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		assert.NilError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	write("main.go", "package main\n")
	write("pkg/util.go", "package pkg // token-123\n")
	write(".git/config", "[core]\n")
	write("vendor/dep/dep.go", "package dep\n")
	write("node_modules/odd\tname.js", "x\n")
	write("image.bin", "\x00\x01")
	assert.NilError(t, os.Symlink("main.go", filepath.Join(dir, "link.go")))

	s, err := source.FromDir(dir, "abc", "token-123")
	assert.NilError(t, err)
	assert.Equal(t, s.Commit, "abc")
	assert.DeepEqual(t, s.Files, map[string]string{
		"main.go":     "package main\n",
		"pkg/util.go": "package pkg // [REDACTED]\n",
	})
	// .git and dependency directories are not walked; binary files and
	// symlinks are skipped without counting as excluded.
	assert.Equal(t, s.Excluded, 0)
}

func TestFromDirSkipsDependencyDirs(t *testing.T) {
	for _, name := range []string{"vendor", "node_modules", ".venv", "__pycache__", "pkg/VENDOR"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600))
			dependency := filepath.Join(dir, name)
			assert.NilError(t, os.MkdirAll(dependency, 0o755))
			t.Cleanup(func() { assert.NilError(t, os.Chmod(dependency, 0o755)) })
			assert.NilError(t, os.Chmod(dependency, 0))
			_, err := os.ReadDir(dependency)
			if err == nil {
				t.Skip("directory permissions are not enforced")
			}
			assert.Assert(t, os.IsPermission(err), err)

			s, err := source.FromDir(dir, "abc")
			assert.NilError(t, err)
			assert.DeepEqual(t, s.Files, map[string]string{"main.go": "package main\n"})
			assert.Equal(t, s.Excluded, 0)
		})
	}
}

const reverseDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -3,3 +3,3 @@
 func A() int {
-	return 1
+	return 2
 }
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1 @@
+package a
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-x
-y
\ No newline at end of file
diff --git a/old/name.txt b/new/name.txt
similarity index 60%
rename from old/name.txt
rename to new/name.txt
--- a/old/name.txt
+++ b/new/name.txt
@@ -1,2 +1,3 @@
 one
-two
\ No newline at end of file
+two
+three
`

func TestReverse(t *testing.T) {
	parsed, err := diff.Parse(reverseDiff)
	assert.NilError(t, err)
	head := &source.Snapshot{Commit: "head", Excluded: 1, Files: map[string]string{
		"a.go":          "package a\n\nfunc A() int {\n\treturn 2\n}\n",
		"new.go":        "package a\n",
		"new/name.txt":  "one\ntwo\nthree\n",
		"untouched.txt": "same\n",
	}}

	before := source.Reverse(head, parsed, "base")
	assert.Equal(t, before.Commit, "base")
	assert.Equal(t, before.Excluded, 1)
	assert.DeepEqual(t, before.Files, map[string]string{
		"a.go":          "package a\n\nfunc A() int {\n\treturn 1\n}\n",
		"gone.txt":      "x\ny",
		"old/name.txt":  "one\ntwo",
		"untouched.txt": "same\n",
	})
	assert.Equal(t, len(head.Files), 4, "head snapshot must not be modified")
}

func TestReverseExcludesMismatchedFiles(t *testing.T) {
	parsed, err := diff.Parse(reverseDiff)
	assert.NilError(t, err)
	head := &source.Snapshot{Commit: "head", Files: map[string]string{
		"a.go":   "package a\n\nfunc A() int {\n\treturn 3\n}\n",
		"new.go": "package a\n",
	}}

	before := source.Reverse(head, parsed, "base")
	// a.go no longer matches the diff and new/name.txt is missing from head.
	assert.DeepEqual(t, before.Files, map[string]string{"gone.txt": "x\ny"})
	assert.Equal(t, before.Excluded, 2)
	assert.DeepEqual(t, before.ExcludedPaths, []string{"a.go", "old/name.txt"})
}

func TestReverseReconcilesExcludedPaths(t *testing.T) {
	parsed, err := diff.Parse(`diff --git a/a.txt b/b.txt
similarity index 100%
rename from a.txt
rename to b.txt
diff --git a/b.txt b/a.txt
similarity index 100%
rename from b.txt
rename to a.txt
diff --git a/added.go b/added.go
new file mode 100644
--- /dev/null
+++ b/added.go
@@ -0,0 +1 @@
+package a
`)
	assert.NilError(t, err)
	// a.txt is too large at head; the old a.txt is rebuilt from head's b.txt.
	head := &source.Snapshot{Commit: "head", Excluded: 3, ExcludedPaths: []string{"a.txt", "added.go", "go.sum"}, Files: map[string]string{"b.txt": "small\n"}}
	before := source.Reverse(head, parsed, "base")
	assert.DeepEqual(t, before.Files, map[string]string{"a.txt": "small\n"})
	assert.DeepEqual(t, before.ExcludedPaths, []string{"b.txt", "go.sum"})
	assert.Equal(t, before.Excluded, 2)
	assert.Assert(t, !before.MayBeExcluded("symlink.txt"))
	encoded, err := json.Marshal(before)
	assert.NilError(t, err)
	_, err = source.Decode(encoded, "base")
	assert.NilError(t, err)
}

func TestReverseExcludesUnredactableKeys(t *testing.T) {
	key := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("Ab1/", 16) + "\n-----END PRIVATE KEY-----\n"
	content, err := json.Marshal(map[string]string{"private_key": key})
	assert.NilError(t, err)
	for _, deleted := range []bool{true, false} {
		t.Run(fmt.Sprintf("deleted=%t", deleted), func(t *testing.T) {
			head := &source.Snapshot{Commit: "head", Files: map[string]string{}}
			raw := "diff --git a/sa.json b/sa.json\n"
			if deleted {
				head.Excluded = 1
				head.ExcludedPaths = []string{"sa.json"}
				raw += "deleted file mode 100644\n--- a/sa.json\n+++ /dev/null\n@@ -1 +0,0 @@\n-" + string(content) + "\n"
			} else {
				head.Files["sa.json"] = "{}\n"
				raw += "--- a/sa.json\n+++ b/sa.json\n@@ -1 +1 @@\n-" + string(content) + "\n+{}\n"
			}
			parsed, err := diff.Parse(security.ScrubDiff(raw))
			assert.NilError(t, err)
			before := source.Reverse(head, parsed, "base")
			assert.Equal(t, len(before.Files), 0)
			assert.Equal(t, before.Excluded, 1)
			assert.DeepEqual(t, before.ExcludedPaths, []string{"sa.json"})
			encoded, err := json.Marshal(before)
			assert.NilError(t, err)
			_, err = source.Decode(encoded, "base")
			assert.NilError(t, err)
		})
	}
}

func TestRedactedKeyDiffWithExcludedSnapshots(t *testing.T) {
	body := func(i int) string { return fmt.Sprintf("MIIC%s%04d", strings.Repeat("Ab1/", 14), i) }
	plusLine := "+" + body(99)
	lines := []string{"package k", "", "const key = `-----BEGIN RSA PRIVATE KEY-----", body(1), body(2), plusLine, body(4), body(5), body(6), body(7), body(8), "-----END RSA PRIVATE KEY-----`", ""}
	lines[7] = "+" + body(50)
	headText := strings.Join(lines, "\n")
	rawDiff := "diff --git a/k.go b/k.go\n--- a/k.go\n+++ b/k.go\n@@ -5,7 +5,7 @@\n " + body(2) + "\n " + plusLine + "\n " + body(4) + "\n-" + body(5) + "\n+" + "+" + body(50) + "\n " + body(6) + "\n " + body(7) + "\n " + body(8) + "\n"

	redactedDiff := security.ScrubDiff(rawDiff)
	assert.Assert(t, !strings.Contains(redactedDiff, "Ab1/"), redactedDiff)
	parsed, err := diff.Parse(redactedDiff)
	assert.NilError(t, err)
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "k.go"), []byte(headText), 0o600))
	head, err := source.FromDir(dir, "head")
	assert.NilError(t, err)
	before := source.Reverse(head, parsed, "base")
	for _, snapshot := range []*source.Snapshot{head, before} {
		assert.Equal(t, len(snapshot.Files), 0)
		assert.Equal(t, snapshot.Excluded, 1)
		assert.DeepEqual(t, snapshot.ExcludedPaths, []string{"k.go"})
		encoded, err := json.Marshal(snapshot)
		assert.NilError(t, err)
		_, err = source.Decode(encoded, snapshot.Commit)
		assert.NilError(t, err)
	}
	assert.Assert(t, parsed.Files[0].Hunks[0].Complete)
	assert.Equal(t, len(parsed.Files[0].Hunks[0].Lines), 8)
}

func TestReverseSkipsDependencyFiles(t *testing.T) {
	parsed, err := diff.Parse(`diff --git a/vendor/dep.go b/vendor/dep.go
--- a/vendor/dep.go
+++ b/vendor/dep.go
@@ -1 +1 @@
-package old
+package dep
diff --git a/node_modules/x.js b/node_modules/x.js
deleted file mode 100644
--- a/node_modules/x.js
+++ /dev/null
@@ -1 +0,0 @@
-x
`)
	assert.NilError(t, err)
	head := &source.Snapshot{Commit: "head", Files: map[string]string{"a.go": "package a\n"}}

	before := source.Reverse(head, parsed, "base")
	assert.DeepEqual(t, before.Files, map[string]string{"a.go": "package a\n"})
	assert.Equal(t, before.Excluded, 0)
}
