package reviewbench

import (
	"encoding/json"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"gotest.tools/v3/assert"
)

func TestChangedOnlyNamesDroppedFiles(t *testing.T) {
	before := &source.Snapshot{Commit: "base", Files: map[string]string{"a.go": "a", "b.go": "b", "z.go": "z"}, Excluded: 1, ExcludedPaths: []string{"big.go"}}
	parsed := &diff.ParsedDiff{Files: []diff.FileDiff{{OldPath: "a.go", NewPath: "a.go", Status: "modified"}}}
	kept := changedOnly(before, parsed)
	assert.DeepEqual(t, kept.Files, map[string]string{"a.go": "a"})
	assert.Equal(t, kept.Excluded, 3)
	assert.DeepEqual(t, kept.ExcludedPaths, []string{"b.go", "big.go", "z.go"})
	assert.DeepEqual(t, before.ExcludedPaths, []string{"big.go"})
	encoded, err := json.Marshal(kept)
	assert.NilError(t, err)
	_, err = source.Decode(encoded, "base")
	assert.NilError(t, err)
}
