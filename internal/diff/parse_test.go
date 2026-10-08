package diff

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestParseValidLines(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want map[string]map[string]bool
	}{
		{
			name: "single file single hunk",
			diff: "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,4 @@\n package foo\n \n+func bar() {}\n func baz() {}\n",
			want: map[string]map[string]bool{
				"foo.go": {"1": true, "2": true, "3": true, "4": true},
			},
		},
		{
			name: "multiple added lines",
			diff: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -5,2 +5,5 @@\n import \"fmt\"\n+import \"os\"\n+import \"io\"\n \n+func init() {}\n",
			want: map[string]map[string]bool{
				"main.go": {"5": true, "6": true, "7": true, "8": true, "9": true},
			},
		},
		{
			name: "multiple files",
			diff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n package a\n+var x = 1\n\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,3 @@\n package b\n+var y = 2\n",
			want: map[string]map[string]bool{
				"a.go": {"1": true, "2": true},
				"b.go": {"1": true, "2": true},
			},
		},
		{
			name: "empty diff",
			diff: "",
			want: map[string]map[string]bool{},
		},
		{
			name: "multiple hunks same file",
			diff: "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,4 @@\n package foo\n \n+func first() {}\n func baz() {}\n@@ -10,3 +11,4 @@\n func existing() {}\n \n+func second() {}\n func end() {}\n",
			want: map[string]map[string]bool{
				"foo.go": {"1": true, "2": true, "3": true, "4": true, "11": true, "12": true, "13": true, "14": true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseValidLines(strings.NewReader(tt.diff))
			assert.NilError(t, err)
			assert.DeepEqual(t, result, tt.want)
		})
	}
}

func TestParseComparisonSides(t *testing.T) {
	text := "diff --git \"a/old name.go\" \"b/new name.go\"\nsimilarity index 50%\nrename from old name.go\nrename to new name.go\n--- \"a/old name.go\"\n+++ \"b/new name.go\"\n@@ -7,2 +9,2 @@ function\n same\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n"
	parsed, err := Parse(text)
	assert.NilError(t, err)
	assert.Equal(t, len(parsed.Files), 1)
	file := parsed.Files[0]
	assert.Equal(t, file.OldPath, "old name.go")
	assert.Equal(t, file.NewPath, "new name.go")
	assert.Equal(t, file.Status, "renamed")
	assert.DeepEqual(t, file.Hunks, []Hunk{{
		OldStart: 7, OldCount: 2, NewStart: 9, NewCount: 2, Complete: true,
		Lines: []DiffLine{
			{Kind: "context", OldLine: 7, NewLine: 9, Content: "same"},
			{Kind: "delete", OldLine: 8, Content: "old", NoNewline: true},
			{Kind: "add", NewLine: 10, Content: "new", NoNewline: true},
		},
	}})
	assert.DeepEqual(t, ValidLines(parsed), map[string]map[string]bool{"new name.go": {"9": true, "10": true}})
}

func TestParseSpecialFiles(t *testing.T) {
	tests := []struct {
		name, text, oldPath, newPath, status string
		binary                               bool
	}{
		{name: "new", text: "diff --git a/new b/new\nnew file mode 100644\n--- /dev/null\n+++ b/new\n@@ -0,0 +1 @@\n+new\n", newPath: "new", status: "added"},
		{name: "deleted", text: "diff --git a/old b/old\ndeleted file mode 100644\n--- a/old\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n", oldPath: "old", status: "deleted"},
		{name: "rename without hunks", text: "diff --git a/old b/new\nsimilarity index 100%\nrename from old\nrename to new\n", oldPath: "old", newPath: "new", status: "renamed"},
		{name: "quoted octal", text: "diff --git \"a/calf\\303\\251\" \"b/calf\\303\\251\"\n--- \"a/calf\\303\\251\"\n+++ \"b/calf\\303\\251\"\n@@ -1 +1 @@\n-old\n+new\n", oldPath: "calf\u00e9", newPath: "calf\u00e9", status: "modified"},
		{name: "spaces", text: "diff --git a/a b.txt b/a b.txt\n--- a/a b.txt\n+++ b/a b.txt\n@@ -1 +1 @@\n-old\n+new\n", oldPath: "a b.txt", newPath: "a b.txt", status: "modified"},
		{name: "space path tab delimiter", text: "diff --git a/a b.txt b/a b.txt\n--- a/a b.txt\t\n+++ b/a b.txt\t\n@@ -1 +1 @@\n-old\n+new\n", oldPath: "a b.txt", newPath: "a b.txt", status: "modified"},
		{name: "mode only ambiguous space", text: "diff --git a/space b/name b/space b/name\nold mode 100644\nnew mode 100755\n", oldPath: "space b/name", newPath: "space b/name", status: "modified"},
		{name: "binary", text: "diff --git a/image b/image\nBinary files a/image and b/image differ\n", oldPath: "image", newPath: "image", status: "modified", binary: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse(tt.text)
			assert.NilError(t, err)
			assert.Equal(t, len(parsed.Files), 1)
			file := parsed.Files[0]
			assert.Equal(t, file.OldPath, tt.oldPath)
			assert.Equal(t, file.NewPath, tt.newPath)
			assert.Equal(t, file.Status, tt.status)
			assert.Equal(t, file.Binary, tt.binary)
			for _, hunk := range file.Hunks {
				assert.Assert(t, hunk.Complete)
			}
		})
	}
}

func TestParseLongAndIncompleteLines(t *testing.T) {
	content := strings.Repeat("x", 100000)
	parsed, err := Parse("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -0,0 +1 @@\n+" + content)
	assert.NilError(t, err)
	assert.Equal(t, parsed.Files[0].Hunks[0].Lines[0].Content, content)
	assert.Assert(t, parsed.Files[0].Hunks[0].Complete)
	parsed, err = Parse("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,3 +1,3 @@\n line\n")
	assert.NilError(t, err)
	assert.Assert(t, !parsed.Files[0].Hunks[0].Complete)
}

func TestParseFileModes(t *testing.T) {
	tests := []struct {
		name, metadata, oldMode, newMode string
	}{
		{name: "remove executable", metadata: "old mode 100755\nnew mode 100644\n", oldMode: "100755", newMode: "100644"},
		{name: "add executable", metadata: "old mode 100644\nnew mode 100755\n", oldMode: "100644", newMode: "100755"},
		{name: "new executable", metadata: "new file mode 100755\n", newMode: "100755"},
		{name: "deleted executable", metadata: "deleted file mode 100755\n", oldMode: "100755"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse("diff --git a/script.sh b/script.sh\n" + tt.metadata)
			assert.NilError(t, err)
			assert.Equal(t, len(parsed.Files), 1)
			assert.Equal(t, parsed.Files[0].OldMode, tt.oldMode)
			assert.Equal(t, parsed.Files[0].NewMode, tt.newMode)
			assert.Equal(t, len(ValidLines(parsed)), 0, "mode changes must not manufacture line anchors")
		})
	}
}

func TestParseRejectsMalformedDiff(t *testing.T) {
	tests := []struct{ name, text string }{
		{name: "overflow", text: "diff --git a/a b/a\n@@ -999999999999999999999999 +1 @@\n-a\n+b\n"},
		{name: "bad hunk", text: "diff --git a/a b/a\n@@ nonsense @@\n"},
		{name: "excess lines", text: "diff --git a/a b/a\n@@ -0,0 +1 @@\n+a\n+b\n"},
		{name: "hunk without file", text: "@@ -1 +1 @@\n-a\n+b\n"},
		{name: "zero coordinate", text: "diff --git a/a b/a\n@@ -0 +1 @@\n-a\n+b\n"},
		{name: "bad quoting", text: "diff --git \"a/b b/b\n"},
		{name: "oversized", text: strings.Repeat("a", maxDiffBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.text)
			assert.Assert(t, err != nil)
		})
	}
}
