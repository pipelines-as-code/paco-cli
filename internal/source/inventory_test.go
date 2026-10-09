package source_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"gotest.tools/v3/assert"
)

const inventoryBefore = `package p

import "fmt"

type S struct{}

func (s *S) Do(x int) int {
	if x == 0 {
		return 0
	}
	return fmt.Sprint(x)
}

func Old() {}
`

const inventoryHead = `package p

import (
	"fmt"
	"strings"
)

type S struct{}

func (s *S) Do(x int) int {
	return fmt.Sprint(x)
}

func New() string { return strings.ToUpper("x") }
`

const inventoryDiff = `diff --git a/p.go b/p.go
--- a/p.go
+++ b/p.go
@@ -1,14 +1,14 @@
 package p
 
-import "fmt"
+import (
+	"fmt"
+	"strings"
+)
 
 type S struct{}
 
 func (s *S) Do(x int) int {
-	if x == 0 {
-		return 0
-	}
 	return fmt.Sprint(x)
 }
 
-func Old() {}
+func New() string { return strings.ToUpper("x") }
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-old
+new
`

func parseDiff(t *testing.T, text string) *source.Diff {
	t.Helper()
	parsed, err := diff.Parse(text)
	assert.NilError(t, err)
	return parsed
}

func TestInventoryBothRevisions(t *testing.T) {
	inv := source.BuildInventory(parseDiff(t, inventoryDiff),
		&source.Snapshot{Files: map[string]string{"p.go": inventoryHead}},
		&source.Snapshot{Files: map[string]string{"p.go": inventoryBefore}})
	assert.Equal(t, inv.Render(), `"p.go" file file-level head-lines:3-6 before-lines:3 hunks:1
"p.go" method (*S).Do modified head:10-12 before:7-12 changed-lines-in:before hunks:1
"p.go" func New added head:14-14 hunks:1
"p.go" func Old deleted before:14-14 hunks:1
Not inventoried: 1 changed non-Go file(s).
`)
}

func TestInventoryHeadOnly(t *testing.T) {
	inv := source.BuildInventory(parseDiff(t, inventoryDiff),
		&source.Snapshot{Files: map[string]string{"p.go": inventoryHead}}, nil)
	assert.Equal(t, inv.Render(), `"p.go" file file-level head-lines:3-6 hunks:1
"p.go" method (*S).Do changed head:10-12 hunks:1
"p.go" func New changed head:14-14 hunks:1
Limitation: "p.go": 2 deleted line(s) in hunk 1 not mapped; before source unavailable
Not inventoried: 1 changed non-Go file(s).
`)
}

func TestInventoryCases(t *testing.T) {
	tests := []struct {
		name   string
		diff   string
		head   map[string]string
		before map[string]string
		want   []string
		nilInv bool
	}{
		{
			name: "rename keeps matching by name",
			diff: "diff --git a/a.go b/b.go\nsimilarity index 90%\nrename from a.go\nrename to b.go\n--- a/a.go\n+++ b/b.go\n" +
				"@@ -1,3 +1,3 @@\n package p\n \n-func F() int { return 1 }\n+func F() int { return 2 }\n",
			head:   map[string]string{"b.go": "package p\n\nfunc F() int { return 2 }\n"},
			before: map[string]string{"a.go": "package p\n\nfunc F() int { return 1 }\n"},
			want:   []string{`"b.go" before-path:"a.go" func F modified head:3-3 before:3-3 changed-lines-in:head+before hunks:1`},
		},
		{
			name: "renamed file deletions keep the before path",
			diff: "diff --git a/a.go b/b.go\nsimilarity index 80%\nrename from a.go\nrename to b.go\n--- a/a.go\n+++ b/b.go\n" +
				"@@ -1,5 +1,3 @@\n package p\n \n-func Old() {}\n-\n func F() {}\n",
			head:   map[string]string{"b.go": "package p\n\nfunc F() {}\n"},
			before: map[string]string{"a.go": "package p\n\nfunc Old() {}\n\nfunc F() {}\n"},
			want:   []string{`"b.go" before-path:"a.go" func Old deleted before:3-3 hunks:1`},
		},
		{
			name: "line directives do not shift declaration ranges",
			diff: "diff --git a/l.go b/l.go\n--- a/l.go\n+++ b/l.go\n@@ -7 +7 @@\n-func G() int { return 1 }\n+func G() int { return 2 }\n",
			head: map[string]string{"l.go": "package p\n\n//line gen.y:1\nfunc F() {\n}\n\nfunc G() int { return 2 }\n"},
			want: []string{`"l.go" func G changed head:7-7 hunks:1`},
		},
		{
			name:   "duplicate init is ambiguous",
			diff:   "diff --git a/i.go b/i.go\n--- a/i.go\n+++ b/i.go\n@@ -5 +5 @@\n-func init() { a() }\n+func init() { b() }\n",
			head:   map[string]string{"i.go": "package p\n\nfunc init() {}\n\nfunc init() { b() }\n"},
			before: map[string]string{"i.go": "package p\n\nfunc init() {}\n\nfunc init() { a() }\n"},
			want: []string{
				`"i.go" func init ambiguous head:5-5 hunks:1`,
				`"i.go" func init ambiguous before:5-5 hunks:1`,
			},
		},
		{
			name:   "closure and grouped values map to their declaration",
			diff:   "diff --git a/c.go b/c.go\n--- a/c.go\n+++ b/c.go\n@@ -3,8 +3,8 @@\n const (\n-\tA = 1\n+\tA = 2\n \tB = 2\n )\n \n var h = func() {\n-\tx()\n+\ty()\n }\n",
			head:   map[string]string{"c.go": "package p\n\nconst (\n\tA = 2\n\tB = 2\n)\n\nvar h = func() {\n\ty()\n}\n"},
			before: map[string]string{"c.go": "package p\n\nconst (\n\tA = 1\n\tB = 2\n)\n\nvar h = func() {\n\tx()\n}\n"},
			want: []string{
				`"c.go" const A modified head:4-4 before:4-4 changed-lines-in:head+before hunks:1`,
				`"c.go" var h modified head:8-10 before:8-10 changed-lines-in:head+before hunks:1`,
			},
		},
		{
			name: "parse failure is reported",
			diff: "diff --git a/bad.go b/bad.go\nnew file mode 100644\n--- /dev/null\n+++ b/bad.go\n@@ -0,0 +1,3 @@\n+package p\n+\n+func F( {\n",
			head: map[string]string{"bad.go": "package p\n\nfunc F( {\n"},
			want: []string{`Limitation: "bad.go": head source failed to parse near line 3`},
		},
		{
			name: "missing snapshot file is reported",
			diff: "diff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ b/gone.go\n@@ -1 +1 @@\n-package a\n+package b\n",
			head: map[string]string{},
			want: []string{`Limitation: "gone.go": head source unavailable (excluded or not collected)`},
		},
		{
			name:   "no Go files",
			diff:   "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-a\n+b\n",
			head:   map[string]string{"README.md": "b\n"},
			nilInv: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var head, before *source.Snapshot
			if tt.head != nil {
				head = &source.Snapshot{Files: tt.head}
			}
			if tt.before != nil {
				before = &source.Snapshot{Files: tt.before}
			}
			inv := source.BuildInventory(parseDiff(t, tt.diff), head, before)
			if tt.nilInv {
				assert.Assert(t, inv == nil)
				return
			}
			rendered := inv.Render()
			for _, want := range tt.want {
				assert.Assert(t, strings.Contains(rendered, want), "missing %q in:\n%s", want, rendered)
			}
		})
	}
}

func TestInventoryNeedsSnapshot(t *testing.T) {
	assert.Assert(t, source.BuildInventory(parseDiff(t, inventoryDiff), nil, nil) == nil)
}

func TestInventoryHunkNumbers(t *testing.T) {
	head := "package p\n\nfunc A() {\n\ta()\n}\n\nfunc B() {\n\tb()\n}\n"
	d := &source.Diff{Files: []source.FileDiff{{OldPath: "h.go", NewPath: "h.go", Status: "modified", Hunks: []source.Hunk{
		{Complete: true, Lines: []source.DiffLine{{Kind: "add", NewLine: 4, Content: "\ta()"}}},
		{Complete: false, Lines: []source.DiffLine{{Kind: "add", NewLine: 8, Content: "\tb()"}}},
	}}}}
	rendered := source.BuildInventory(d, &source.Snapshot{Files: map[string]string{"h.go": head}}, nil).Render()
	assert.Assert(t, strings.Contains(rendered, `"h.go" func A changed head:3-5 hunks:1`+"\n"), rendered)
	assert.Assert(t, strings.Contains(rendered, `"h.go" func B changed head:7-9 hunks:2`+"\n"), rendered)
	assert.Assert(t, strings.Contains(rendered, `hunk 2 is incomplete`), rendered)
}

func TestInventoryTruncates(t *testing.T) {
	var content strings.Builder
	content.WriteString("package p\n")
	var lines []source.DiffLine
	lines = append(lines, source.DiffLine{Kind: "add", NewLine: 1, Content: "package p"})
	for i := 0; i < 1000; i++ {
		line := fmt.Sprintf("func LongFunctionName%04d() {}", i)
		content.WriteString(line + "\n")
		lines = append(lines, source.DiffLine{Kind: "add", NewLine: i + 2, Content: line})
	}
	d := &source.Diff{Files: []source.FileDiff{{NewPath: "big.go", Status: "added", Hunks: []source.Hunk{{Complete: true, Lines: lines}}}}}
	inv := source.BuildInventory(d, &source.Snapshot{Files: map[string]string{"big.go": content.String()}}, nil)
	assert.Equal(t, len(inv.Entries), 1001)
	rendered := inv.Render()
	assert.Assert(t, strings.Contains(rendered, "[Inventory truncated:"), rendered)
	assert.Assert(t, len(rendered) < 15000)
	assert.Assert(t, strings.Contains(rendered, `"big.go" func LongFunctionName0000 added head:2-2 hunks:1`))
}
