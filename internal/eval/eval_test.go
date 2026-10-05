package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"gotest.tools/v3/assert"
)

func TestPrepareCorpus(t *testing.T) {
	cases, err := Load("../review/testdata/eval/cases.json")
	assert.NilError(t, err)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			ws := &artifact.Workspace{Dir: t.TempDir()}
			digest, err := Prepare(ws, c)
			assert.NilError(t, err)
			assert.Equal(t, len(digest), 64)
			data, err := ws.Read(artifact.FileDiff)
			assert.NilError(t, err)
			parsed, err := diff.Parse(string(data))
			assert.NilError(t, err)
			assert.Assert(t, len(parsed.Files) > 0)
			assert.Assert(t, !ws.Exists(artifact.FileReview))
			// Expected issue IDs are labels, not model context.
			entries, err := os.ReadDir(ws.Dir)
			assert.NilError(t, err)
			for _, entry := range entries {
				content, err := os.ReadFile(filepath.Join(ws.Dir, entry.Name()))
				assert.NilError(t, err)
				for _, issue := range c.Issues {
					assert.Assert(t, !strings.Contains(string(content), issue.ID))
				}
			}
		})
	}
}

func TestScoreRequiresAdjudication(t *testing.T) {
	report := Report{Version: 1, Runs: []Run{
		{
			ID: "positive", Expected: []Issue{{ID: "bug"}, {ID: "missed"}},
			Review: review.Review{Comments: []review.Comment{{Body: "one"}, {Body: "duplicate"}, {Body: "noise"}}},
		},
		{ID: "negative", Expected: []Issue{}, Review: review.Review{Comments: []review.Comment{{Body: "noise"}}}},
		{ID: "failed", Failed: true, Expected: []Issue{{ID: "hidden"}}},
	}}
	yes, no := true, false
	judgments := Judgments{
		Findings: []Judgment{
			{RunID: "positive", Finding: 0, Issue: "bug", AnchorValid: &yes},
			{RunID: "positive", Finding: 1, Issue: "bug", AnchorValid: &yes},
			{RunID: "positive", Finding: 2, Issue: "false_positive", AnchorValid: &no},
			{RunID: "negative", Finding: 0, Issue: "false_positive", AnchorValid: &yes},
		},
		Summaries: []SummaryJudgment{
			{RunID: "positive", Accurate: &yes}, {RunID: "negative", Accurate: &no}, {RunID: "failed", Accurate: &yes},
		},
	}
	m, err := Score(report, judgments)
	assert.NilError(t, err)
	assert.Equal(t, m.TruePositives, 1)
	assert.Equal(t, m.FalsePositives, 3)
	assert.Equal(t, m.Missed, 2)
	assert.Equal(t, m.Duplicates, 1)
	assert.Equal(t, m.FailedRuns, 1)
	assert.Equal(t, m.NegativeFalseRuns, 1)
	assert.Equal(t, m.Precision, 0.25)
	assert.Equal(t, m.Recall, 1.0/3.0)
	assert.Equal(t, m.ValidAnchors, 3)
	assert.Equal(t, m.AnchorsAssessed, 4)
	assert.Equal(t, m.AccurateSummaries, 2)
	assert.Equal(t, m.SummariesAssessed, 3)
	missing := judgments
	missing.Findings = judgments.Findings[:2]
	_, err = Score(report, missing)
	assert.ErrorContains(t, err, "one judgment per finding")
	invalid := judgments
	invalid.Findings = append([]Judgment{}, judgments.Findings...)
	invalid.Findings[0].Issue = "invented"
	_, err = Score(report, invalid)
	assert.ErrorContains(t, err, "unknown issue")
	duplicate := judgments
	duplicate.Findings = append(append([]Judgment{}, judgments.Findings...), judgments.Findings[0])
	_, err = Score(report, duplicate)
	assert.ErrorContains(t, err, "duplicate judgment")
	missing = judgments
	missing.Summaries = nil
	_, err = Score(report, missing)
	assert.ErrorContains(t, err, "summary accuracy")
}
