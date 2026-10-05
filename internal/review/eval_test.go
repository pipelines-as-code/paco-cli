package review

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// These tests check the fixture contract and deterministic validation only.
// They are not measurements of a model's ability to find the labeled defects.
func TestEvaluationCorpus(t *testing.T) {
	var cases []struct {
		Name   string            `json:"name"`
		Split  string            `json:"split"`
		Before map[string]string `json:"before"`
		Head   map[string]string `json:"head"`
		Issues []struct {
			ID    string `json:"id"`
			Path  string `json:"path"`
			Line  int    `json:"line"`
			Side  string `json:"side"`
			Claim string `json:"claim"`
		} `json:"issues"`
	}
	data, err := os.ReadFile("testdata/eval/cases.json")
	assert.NilError(t, err)
	assert.NilError(t, json.Unmarshal(data, &cases))
	assert.Assert(t, len(cases) >= 30)
	names := map[string]bool{}
	negative, crossFile, holdout := 0, 0, 0
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			assert.Assert(t, !names[c.Name], "duplicate fixture name")
			names[c.Name] = true
			assert.Assert(t, c.Split == "tuning" || c.Split == "holdout")
			assert.Assert(t, c.Before != nil && c.Head != nil && c.Issues != nil)
			if c.Split == "holdout" {
				holdout++
			}
			if len(c.Issues) == 0 {
				negative++
			}
			if len(c.Before) > 1 || len(c.Head) > 1 {
				crossFile++
			}
			for _, issue := range c.Issues {
				files := c.Head
				if issue.Side == "before" {
					files = c.Before
				}
				assert.Assert(t, issue.Side == "head" || issue.Side == "before")
				assert.Assert(t, issue.ID != "" && issue.Claim != "")
				content, ok := files[issue.Path]
				assert.Assert(t, ok)
				assert.Assert(t, issue.Line > 0 && issue.Line <= len(strings.Split(content, "\n")))
				assert.Assert(t, c.Before[issue.Path] != c.Head[issue.Path], "issue must concern a changed file")
			}
		})
	}
	assert.Assert(t, negative >= 10)
	assert.Assert(t, crossFile >= 10)
	assert.Assert(t, holdout >= 10)
}
