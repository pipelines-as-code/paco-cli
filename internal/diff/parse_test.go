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
				"foo.go": {"3": true},
			},
		},
		{
			name: "multiple added lines",
			diff: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -5,2 +5,5 @@\n import \"fmt\"\n+import \"os\"\n+import \"io\"\n \n+func init() {}\n",
			want: map[string]map[string]bool{
				"main.go": {"6": true, "7": true, "9": true},
			},
		},
		{
			name: "multiple files",
			diff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n package a\n+var x = 1\n\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,3 @@\n package b\n+var y = 2\n",
			want: map[string]map[string]bool{
				"a.go": {"2": true},
				"b.go": {"2": true},
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
				"foo.go": {"3": true, "13": true},
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
