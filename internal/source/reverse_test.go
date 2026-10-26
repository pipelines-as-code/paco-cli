package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
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
	// .git and dependency directories are not walked; binary and symlink count as excluded.
	assert.Equal(t, s.Excluded, 2)
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
