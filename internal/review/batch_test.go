package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"gotest.tools/v3/assert"
)

func largeTestDiff(files, lines int) string {
	var patch strings.Builder
	for f := range files {
		fmt.Fprintf(&patch, "diff --git a/file%d.go b/file%d.go\nnew file mode 100644\n--- /dev/null\n+++ b/file%d.go\n@@ -0,0 +1,%d @@\n", f, f, f, lines)
		for n := range lines {
			fmt.Fprintf(&patch, "+var marker%d_%d = %q\n", f, n, strings.Repeat("x", 270))
		}
	}
	return patch.String()
}

func TestReviewBatchesPreserveEveryLine(t *testing.T) {
	for _, tc := range []struct {
		name         string
		files, lines int
	}{
		{"whole files", 3, 320},
		{"oversized hunk", 1, 1500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := diff.Parse(largeTestDiff(tc.files, tc.lines))
			assert.NilError(t, err)
			batches, overview, err := reviewBatches(parsed)
			assert.NilError(t, err)
			assert.Assert(t, len(batches) > 1)
			seen := map[string]int{}
			for _, batch := range batches {
				assert.Assert(t, len(batch) <= batchBytes)
				for _, line := range strings.Split(batch, "\n") {
					if _, content, ok := strings.Cut(line, " source_json="); ok {
						var source string
						assert.NilError(t, json.Unmarshal([]byte(content), &source))
						seen[source]++
					}
				}
			}
			assert.Equal(t, len(seen), tc.files*tc.lines)
			joined := strings.Join(batches, "")
			for _, file := range parsed.Files {
				assert.Assert(t, strings.Contains(overview, file.NewPath))
				for _, hunk := range file.Hunks {
					for _, line := range hunk.Lines {
						assert.Equal(t, seen[line.Content], 1)
						content, _ := json.Marshal(line.Content)
						assert.Assert(t, strings.Contains(joined, fmt.Sprintf("add before:0 head:%d no_newline:false source_json=%s", line.NewLine, content)))
					}
				}
			}
		})
	}
}

func TestReviewBatchesKeepHunksAndMetadata(t *testing.T) {
	patch := "diff --git a/old.go b/new.go\nsimilarity index 50%\nrename from old.go\nrename to new.go\n--- a/old.go\n+++ b/new.go\n"
	for _, n := range []int{10, 2000} {
		patch += fmt.Sprintf("@@ -%d,300 +%d,300 @@\n", n, n)
		patch += strings.Repeat("-"+strings.Repeat("o", 150)+"\n", 300)
		patch += strings.Repeat("+"+strings.Repeat("n", 150)+"\n", 300)
	}
	patch += "diff --git a/image b/image\nBinary files a/image and b/image differ\n"
	patch += "diff --git a/deleted b/deleted\ndeleted file mode 100644\n--- a/deleted\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n"
	parsed, err := diff.Parse(patch)
	assert.NilError(t, err)
	batches, overview, err := reviewBatches(parsed)
	assert.NilError(t, err)
	assert.Equal(t, len(batches), 2)
	assert.Assert(t, strings.Contains(batches[0], "delete before:10 head:0"))
	assert.Assert(t, !strings.Contains(batches[0], "delete before:2000 head:0"))
	assert.Assert(t, strings.Contains(batches[1], "delete before:2000 head:0"))
	assert.Assert(t, strings.Contains(overview, `before="old.go" head="new.go" status=renamed`))
	assert.Assert(t, strings.Contains(overview, `head="image" status=modified binary=true`))
	assert.Assert(t, strings.Contains(batches[1], `before="deleted" head="" status=deleted`))
}

func TestReviewBatchesKeepFinalNewlineMarker(t *testing.T) {
	render := func(patch string) string {
		parsed, err := diff.Parse(patch)
		assert.NilError(t, err)
		batches, _, err := reviewBatches(parsed)
		assert.NilError(t, err)
		assert.Equal(t, len(batches), 1)
		return batches[0]
	}
	header := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n"
	removed := render(header + "-hello\n+hello\n\\ No newline at end of file\n")
	added := render(header + "-hello\n\\ No newline at end of file\n+hello\n")
	assert.Assert(t, removed != added)
	assert.Assert(t, strings.Contains(removed, `add before:0 head:1 no_newline:true source_json="hello"`))
	assert.Assert(t, strings.Contains(added, `delete before:1 head:0 no_newline:true source_json="hello"`))
}

func TestMergeBatchReviewsKeepsHighestSeverityDuplicate(t *testing.T) {
	first := &Review{Comments: []Comment{{Path: "a.go", Line: 1, Body: "A", Severity: "low"}}}
	var others []Comment
	for i := range maxComments {
		others = append(others, Comment{Path: "b.go", Line: i + 1, Body: fmt.Sprintf("other %d", i), Severity: "medium"})
	}
	later := &Review{Comments: append(others, Comment{Path: "a.go", Line: 1, Body: "A", Severity: "CRITICAL"})}
	merged := Normalize(mergeBatchReviews([]*Review{first, later}, &Review{}))
	assert.Equal(t, len(merged.Comments), maxComments)
	assert.DeepEqual(t, merged.Comments[0], Comment{Path: "a.go", Line: 1, Body: "A", Severity: "critical"})
}

func TestCandidateSourcesBoundsRepeatedReferences(t *testing.T) {
	path := strings.Repeat("a/", 210) + "x.go"
	evidence := evidenceContext{
		lines:   map[string]map[string]map[int]string{"head": {path: {1: "x"}}},
		changed: map[string]map[string]map[int]bool{"head": {path: {1: true}}},
		shown:   map[string]map[string]map[int]bool{"head": {path: {1: true}}},
	}
	var candidates []Candidate
	for range 30 {
		candidate := Candidate{Path: path, Line: 1, Side: "head"}
		for range 12 {
			candidate.Evidence = append(candidate.Evidence, Evidence{Revision: "head", Path: path, Start: 1, End: 1, Quote: "x"})
		}
		candidates = append(candidates, candidate)
	}
	output, err := candidateSources(evidence, candidates)
	assert.NilError(t, err)
	assert.Assert(t, len(output) <= batchBytes)
	assert.Equal(t, strings.Count(output, "Revision=head"), 1)
	assert.Equal(t, strings.Count(output, "line:1 "), 1)
}

func TestRunLargeDiffBatchesAndIntegrates(t *testing.T) {
	patch := largeTestDiff(3, 320)
	parsed, err := diff.Parse(patch)
	assert.NilError(t, err)
	batches, _, err := reviewBatches(parsed)
	assert.NilError(t, err)
	var responses []string
	for i := range batches {
		report := Review{Summary: fmt.Sprintf("batch %d", i), Comments: []Comment{}, ReviewScore: ReviewScore{Rating: 3, Reason: "change"}}
		if i == len(batches)-1 {
			report.Comments = []Comment{{Path: "file2.go", Line: 320, Severity: "high", Body: "A finding from the last batch"}}
			report.SecuritySensitive = true
		}
		responses = append(responses, "Discard this batch prose\n"+jsonText(t, report))
	}
	responses = append(responses, `{"summary":"Whole PR","comments":[],"review_score":{"rating":4,"reason":"change"},"security_sensitive":false}`)
	resolve, requests := reviewServer(t, responses)
	ws := setupWorkspaceWithDiff(t, patch)
	budget := model.NewBudget()
	var logs bytes.Buffer
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, Budget: budget, LogWriter: &logs}))
	assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileFailed)), logs.String())
	assert.Equal(t, len(*requests), len(batches)+1)
	assert.Equal(t, budget.Snapshot().Usage.ModelRequests, int64(len(batches)+1))
	result := readReview(t, ws)
	assert.Equal(t, result.Comments[0].Path, "file2.go")
	assert.Equal(t, result.Comments[0].Line, 320)
	assert.Assert(t, result.SecuritySensitive)
	first := jsonText(t, (*requests)[0])
	assert.Assert(t, strings.Contains(first, "Whole PR change map"))
	assert.Assert(t, strings.Contains(first, "read_diff"))
	last := jsonText(t, (*requests)[len(batches)])
	assert.Assert(t, !strings.Contains(last, "Discard this batch prose"))
	for i := range batches {
		assert.Assert(t, strings.Contains(last, fmt.Sprintf("Batch %d report", i+1)))
	}
}

func TestRunLargeDiffFailsClosed(t *testing.T) {
	valid := `{"summary":"batch","comments":[]}`
	for _, tc := range []struct {
		name     string
		response []string
		turns    int64
		want     int
	}{
		{"malformed later batch", []string{valid, "not a review"}, 24, 2},
		{"refused batch", []string{""}, 24, 1},
		{"secret in batch", []string{`{"summary":"test-model-key","comments":[]}`}, 24, 1},
		{"escaped secret in batch", []string{`{"summary":"\u0074est-model-key","comments":[]}`}, 24, 1},
		{"insufficient budget", nil, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolve, requests := reviewServer(t, tc.response)
			ws := setupWorkspaceWithDiff(t, largeTestDiff(3, 320))
			budget, err := model.NewBudgetWithLimits(model.Limits{Turns: tc.turns, ToolCalls: 80})
			assert.NilError(t, err)
			assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, Budget: budget, LogWriter: &bytes.Buffer{}}))
			assert.Equal(t, len(*requests), tc.want)
			if strings.Contains(tc.name, "secret") {
				assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileSecurityBlock)))
			} else {
				assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
			}
			assert.Equal(t, len(readReview(t, ws).Comments), 0)
		})
	}
}

func largeVerifiedWorkspace(t *testing.T, patch string) string {
	t.Helper()
	ws := setupWorkspaceWithDiff(t, patch)
	manifest := artifact.InputManifest{
		Version: 1, Repo: "owner/repo", PRNumber: 1, HeadSHA: "head-sha", BaseRef: "main",
		DiffDigest: Digest([]byte(patch)), ContextStatus: "partial",
		Head: artifact.ContextState{Status: "unavailable"}, Before: artifact.ContextState{Status: "unavailable"},
	}
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileInputManifest), []byte(jsonText(t, manifest)), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileHeadSHA), []byte("head-sha"), 0o600))
	return ws
}

func TestVerifiedLargeDiffUsesSharedBudget(t *testing.T) {
	patch := largeTestDiff(3, 320)
	parsed, err := diff.Parse(patch)
	assert.NilError(t, err)
	batches, _, err := reviewBatches(parsed)
	assert.NilError(t, err)
	ws := largeVerifiedWorkspace(t, patch)
	var responses []string
	candidate := Candidate{
		ID: "late", Path: "file2.go", Line: 320, Side: "head", Severity: "high",
		Claim: "A late finding", Trigger: "input", Impact: "failure", Remedy: "fix",
		Evidence: []Evidence{{Revision: "head", Path: "file2.go", Start: 320, End: 320, Quote: parsed.Files[2].Hunks[0].Lines[319].Content}},
	}
	early := candidate
	early.Path, early.Line, early.Claim = "file0.go", 1, "An early finding"
	early.Evidence = []Evidence{{Revision: "head", Path: "file0.go", Start: 1, End: 1, Quote: parsed.Files[0].Hunks[0].Lines[0].Content}}
	for i := range len(batches) + 1 {
		report := discovery{Summary: "Whole PR", Candidates: []Candidate{}, ReviewScore: ReviewScore{Rating: 3, Reason: "change"}}
		if i == 0 {
			report.Candidates = []Candidate{early}
		}
		if i == len(batches)-1 {
			report.Candidates = []Candidate{candidate}
			report.SecuritySensitive, report.Overflow = true, true
		}
		responses = append(responses, jsonText(t, report))
	}
	responses = append(responses, jsonText(t, verdict{Summary: "Whole PR", Decisions: []decision{
		{ID: "b1-1", Outcome: "accept", Reason: "confirmed", Severity: "high", Evidence: early.Evidence},
		{ID: fmt.Sprintf("b%d-1", len(batches)), Outcome: "accept", Reason: "confirmed", Severity: "high", Evidence: candidate.Evidence},
	}}))
	resolve, requests := reviewServer(t, responses)
	budget := model.NewBudget()
	var logs bytes.Buffer
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, VerifyFindings: true, Budget: budget, LogWriter: &logs}))
	assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileFailed)), logs.String())
	assert.Equal(t, len(*requests), len(batches)+2)
	assert.Equal(t, budget.Snapshot().Usage.ModelRequests, int64(len(batches)+2))
	assert.Assert(t, readReview(t, ws).Verified)
	assert.Equal(t, len(readReview(t, ws).Comments), 2)
	assert.Equal(t, readReview(t, ws).Comments[1].Path, "file2.go")
	assert.Equal(t, readReview(t, ws).Comments[1].Line, 320)
	assert.Assert(t, readReview(t, ws).SecuritySensitive)
	status, err := os.ReadFile(filepath.Join(ws, artifact.FileStatus))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(status), "Discovery reached the candidate limit"))
	assert.Assert(t, strings.Contains(jsonText(t, (*requests)[len(batches)+1]), "Source excerpts from collected revisions"))
}

func TestVerifiedLargeDiffRejectsInvalidBatch(t *testing.T) {
	for _, response := range []string{"malformed", `{"summary":"test-model-key","candidates":[]}`} {
		t.Run(response, func(t *testing.T) {
			ws := largeVerifiedWorkspace(t, largeTestDiff(3, 320))
			resolve, requests := reviewServer(t, []string{response})
			assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, VerifyFindings: true, LogWriter: &bytes.Buffer{}}))
			assert.Equal(t, len(*requests), 1)
			assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
			assert.Equal(t, len(readReview(t, ws).Comments), 0)
			if strings.Contains(response, "test-model-key") {
				assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileSecurityBlock)))
			}
		})
	}
}

func TestLargeDiffCommentCapUsesValidSeverityOrder(t *testing.T) {
	for _, count := range []int{29, 30} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			invalid := make([]Comment, 30)
			for i := range invalid {
				invalid[i] = Comment{Line: 1, Severity: "critical", Body: fmt.Sprintf("invalid %d", i)}
			}
			var low []Comment
			for i := range count {
				low = append(low, Comment{Path: "file0.go", Line: 1, Severity: "low", Body: fmt.Sprintf("finding %d", i)})
			}
			report := func(comments []Comment) string {
				return jsonText(t, Review{Summary: "change", Comments: comments, ReviewScore: ReviewScore{Rating: 3, Reason: "change"}})
			}
			resolve, _ := reviewServer(t, []string{report(invalid), report(low), report([]Comment{}), report([]Comment{
				{Path: "file2.go", Line: 320, Severity: "critical", Body: "integration finding"},
			})})
			ws := setupWorkspaceWithDiff(t, largeTestDiff(3, 320))
			var logs bytes.Buffer
			assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, LogWriter: &logs}))
			assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileFailed)), logs.String())
			review := readReview(t, ws)
			assert.Equal(t, len(review.Comments), maxComments)
			assert.Equal(t, review.Comments[0].Body, "integration finding")
			assert.Equal(t, strings.Contains(logs.String(), "Review reached the comment limit"), count == 30)
		})
	}
}

func TestLargeDiffCapSkipsCommentsWithoutDiffAnchor(t *testing.T) {
	var detached []Comment
	for i := range maxComments {
		detached = append(detached, Comment{Path: "missing.go", Line: 1, Severity: "critical", Body: fmt.Sprintf("detached %d", i)})
	}
	var low []Comment
	for i := range maxComments {
		low = append(low, Comment{Path: "file0.go", Line: 1, Severity: "low", Body: fmt.Sprintf("finding %d", i)})
	}
	report := func(comments []Comment) string {
		return jsonText(t, Review{Summary: "change", Comments: comments, ReviewScore: ReviewScore{Rating: 3, Reason: "change"}})
	}
	resolve, _ := reviewServer(t, []string{report(detached), report(low), report([]Comment{}), report([]Comment{
		{Path: "file2.go", Line: 320, Severity: "critical", Body: "integration finding"},
	})})
	ws := setupWorkspaceWithDiff(t, largeTestDiff(3, 320))
	var logs bytes.Buffer
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, LogWriter: &logs}))
	review := readReview(t, ws)
	assert.Equal(t, len(review.Comments), maxComments)
	assert.Equal(t, review.Comments[0].Body, "integration finding")
	for _, comment := range review.Comments {
		assert.Assert(t, comment.Path != "missing.go", "unanchored comment kept: %s", comment.Body)
	}
}

func TestVerifiedDuplicateKeepsSevereCopyBeforeCap(t *testing.T) {
	patch := largeTestDiff(3, 320)
	parsed, err := diff.Parse(patch)
	assert.NilError(t, err)
	base := Candidate{
		Path: "file0.go", Side: "head", Line: 1, Severity: "low",
		Claim: "duplicate claim", Trigger: "input", Impact: "failure", Remedy: "fix",
		Evidence: []Evidence{{Revision: "head", Path: "file0.go", Start: 1, End: 1, Quote: parsed.Files[0].Hunks[0].Lines[0].Content}},
	}
	var mediums []Candidate
	for i := range maxComments {
		candidate := base
		candidate.ID, candidate.Claim, candidate.Severity = fmt.Sprintf("m%d", i), fmt.Sprintf("medium claim %d", i), "medium"
		mediums = append(mediums, candidate)
	}
	low := base
	low.ID = "dup-low"
	critical := base
	critical.ID, critical.Severity = "dup-critical", "critical"
	report := func(candidates []Candidate) string {
		return jsonText(t, discovery{Summary: "change", Candidates: candidates, ReviewScore: ReviewScore{Rating: 3, Reason: "change"}})
	}
	decisions := []decision{{ID: "b4-1", Outcome: "accept", Reason: "confirmed", Severity: "critical", Evidence: base.Evidence}}
	for i := range maxComments - 1 {
		decisions = append(decisions, decision{ID: fmt.Sprintf("b2-%d", i+1), Outcome: "accept", Reason: "confirmed", Severity: "medium", Evidence: base.Evidence})
	}
	resolve, _ := reviewServer(t, []string{report([]Candidate{low}), report(mediums), report([]Candidate{}), report([]Candidate{critical}), jsonText(t, verdict{Summary: "change", Decisions: decisions})})
	ws := largeVerifiedWorkspace(t, patch)
	var logs bytes.Buffer
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, VerifyFindings: true, LogWriter: &logs}))
	review := readReview(t, ws)
	assert.Equal(t, len(review.Comments), maxComments)
	assert.Equal(t, review.Comments[0].Severity, "critical")
	assert.Assert(t, strings.Contains(review.Comments[0].Body, "duplicate claim"), review.Comments[0].Body)
	var status VerificationStatus
	data, err := os.ReadFile(filepath.Join(ws, artifact.FileStatus))
	assert.NilError(t, err)
	assert.NilError(t, json.Unmarshal(data, &status))
	assert.Equal(t, status.Duplicates, 1)
}

func TestVerifiedBatchCapFollowsEvidenceValidation(t *testing.T) {
	patch := largeTestDiff(3, 320)
	parsed, err := diff.Parse(patch)
	assert.NilError(t, err)
	base := Candidate{
		ID: "candidate", Path: "file0.go", Side: "head", Line: 1, Severity: "low",
		Claim: "claim", Trigger: "input", Impact: "failure", Remedy: "fix",
		Evidence: []Evidence{{Revision: "head", Path: "file0.go", Start: 1, End: 1, Quote: parsed.Files[0].Hunks[0].Lines[0].Content}},
	}
	var invalid, valid []Candidate
	for i := range maxComments {
		candidate := base
		candidate.ID, candidate.Claim = fmt.Sprintf("c%d", i), fmt.Sprintf("claim %d", i)
		valid = append(valid, candidate)
		candidate.Path = "missing.go"
		invalid = append(invalid, candidate)
	}
	critical := base
	critical.Path, critical.Line, critical.Severity, critical.Claim = "file2.go", 320, "critical", "integration finding"
	critical.Evidence = []Evidence{{Revision: "head", Path: "file2.go", Start: 320, End: 320, Quote: parsed.Files[2].Hunks[0].Lines[319].Content}}
	report := func(candidates []Candidate) string {
		return jsonText(t, discovery{Summary: "change", Candidates: candidates, ReviewScore: ReviewScore{Rating: 3, Reason: "change"}})
	}
	decisions := []decision{{ID: "b4-1", Outcome: "accept", Reason: "confirmed", Severity: "critical", Evidence: critical.Evidence}}
	for i := range maxComments - 1 {
		decisions = append(decisions, decision{ID: fmt.Sprintf("b2-%d", i+1), Outcome: "accept", Reason: "confirmed", Severity: "low", Evidence: base.Evidence})
	}
	resolve, _ := reviewServer(t, []string{report(invalid), report(valid), report([]Candidate{}), report([]Candidate{critical}), jsonText(t, verdict{Summary: "change", Decisions: decisions})})
	ws := largeVerifiedWorkspace(t, patch)
	var logs bytes.Buffer
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: resolve, VerifyFindings: true, LogWriter: &logs}))
	assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileFailed)), logs.String())
	assert.Equal(t, len(readReview(t, ws).Comments), maxComments)
	assert.Equal(t, readReview(t, ws).Comments[0].Severity, "critical")
	var status VerificationStatus
	data, err := os.ReadFile(filepath.Join(ws, artifact.FileStatus))
	assert.NilError(t, err)
	assert.NilError(t, json.Unmarshal(data, &status))
	assert.Equal(t, status.Rejected, maxComments)
	assert.Assert(t, strings.Contains(strings.Join(status.Limitations, "\n"), "Discovery reached the candidate limit"))
}
