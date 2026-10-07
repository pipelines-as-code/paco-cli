package review

import (
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"gotest.tools/v3/assert"
)

const suggestionDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 func f(x *T) {
+	v := x.Value
 	return v
 }
`

func TestGuardSuggestions(t *testing.T) {
	fence := func(lines string) string { return "Nil dereference.\n\n```suggestion\n" + lines + "```\n" }
	tests := []struct {
		name string
		body string
		want string
		drop bool
	}{
		{name: "valid single line", body: fence("\tif x == nil {\n"), want: fence("\tif x == nil {\n")},
		{name: "valid expansion", body: fence("\tif x == nil {\n\t\treturn 0\n\t}\n\tv := x.Value\n"), want: fence("\tif x == nil {\n\t\treturn 0\n\t}\n\tv := x.Value\n")},
		{name: "no-op replacement", body: fence("\tv := x.Value  \n"), want: "Nil dereference."},
		{name: "duplicates following line", body: fence("\tv := x.Value()\n\treturn v\n"), want: "Nil dereference."},
		{name: "duplicates preceding line", body: fence("func f(x *T) {\n\tv := x.Value()\n"), want: "Nil dereference."},
		{name: "unclosed fence", body: "Nil dereference.\n\n```suggestion\n\tv := x.Value()\n", want: "Nil dereference."},
		{name: "two fences", body: fence("a\n") + fence("b\n"), want: "Nil dereference.\n\nNil dereference."},
		{name: "suggestion only", body: "```suggestion\n\tv := x.Value\n```", drop: true},
		{name: "no suggestion", body: "Plain finding.", want: "Plain finding."},
		{name: "line deletion", body: fence(""), want: fence("")},
	}
	parsed, err := diff.Parse(suggestionDiff)
	assert.NilError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := guardSuggestions([]Comment{{Path: "a.go", Line: 2, Body: tt.body}}, parsed)
			if tt.drop {
				assert.Equal(t, len(got), 0)
				return
			}
			assert.Equal(t, len(got), 1)
			assert.Equal(t, got[0].Body, tt.want)
		})
	}
}

func TestGuardSuggestionsUnknownAnchorKeepsWellFormedFence(t *testing.T) {
	body := "Fix.\n\n```suggestion\nx\ny\n```"
	got := guardSuggestions([]Comment{{Path: "other.go", Line: 9, Body: body}}, nil)
	assert.Equal(t, got[0].Body, body)
}
