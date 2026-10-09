package post

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"gotest.tools/v3/assert"
)

func verifiedArtifacts(t *testing.T) map[string]string {
	t.Helper()
	rev := review.Review{
		Verified: true, Summary: "Changes behavior.",
		ReviewScore: review.ReviewScore{Rating: 2, Reason: "Small change."},
		Comments: []review.Comment{
			{Path: "a.go", Line: 20, Severity: "high", Body: "A different issue on this line."},
			{Path: "a.go", Line: 20, Severity: "medium", Body: "Another distinct issue on the same line."},
		},
		SummaryFindings: []review.Comment{{Path: "old.go", Line: 2, Severity: "high", Body: "Deleted cleanup leaks resources."}},
	}
	data, err := json.Marshal(rev)
	assert.NilError(t, err)
	status := review.VerificationStatus{
		Version: 1, State: "complete", Repo: "owner/repo",
		PRNumber: 1, HeadSHA: "abc123", ReviewDigest: review.Digest(data), Accepted: 3, Unanchored: 1,
		BaseRef: "main", TargetBaseSHA: "def456",
		Decisions: []review.Disposition{
			{ID: "leak", Outcome: "accept", Reason: "Confirmed."},
			{ID: "nil-deref", Outcome: "reject", Reason: "Caller guards\nagainst nil."},
		},
	}
	statusData, err := json.Marshal(status)
	assert.NilError(t, err)
	return map[string]string{
		artifact.FileReview: string(data), artifact.FileHeadSHA: "abc123",
		artifact.FileValidLines:     `{"a.go":{"20":true}}`,
		artifact.FileExistingInline: `{"a.go":{"20":true}}`,
		review.FileStatus:           string(statusData),
	}
}

func TestPostVerified(t *testing.T) {
	f, gh := ghtest.New(t)
	happy(f)
	f.JSON("GET /repos/owner/repo/pulls/1", `{"head":{"sha":"abc123"},"base":{"ref":"main","sha":"def456"}}`)
	ws := workspace(t, verifiedArtifacts(t))
	var logs bytes.Buffer
	opts := Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh, LogWriter: &logs}
	assert.NilError(t, Run(context.Background(), opts))
	calls := f.Calls("POST " + reviewsPath)
	assert.Equal(t, len(calls), 1)
	assert.Assert(t, strings.Contains(calls[0].Body, "A different issue"))
	assert.Assert(t, strings.Contains(calls[0].Body, "Another distinct issue"))
	body := f.Calls("POST " + commentsPath)[0].Body
	assert.Assert(t, strings.Contains(body, "before line 2"))
	assert.Assert(t, strings.Contains(body, "3 verified findings"))
	assert.Assert(t, strings.Contains(body, "<summary>1 candidates not published</summary>"))
	assert.Assert(t, strings.Contains(body, "- `nil-deref`: reject. Caller guards against nil."))
	assert.Assert(t, !strings.Contains(body, "`leak`"))
	data, err := os.ReadFile(filepath.Join(ws, artifact.FileReview))
	assert.NilError(t, err)
	status, err := review.ReadStatus(&artifact.Workspace{Dir: ws}, data)
	assert.NilError(t, err)
	assert.Equal(t, status.Posted, 2)
	assert.Assert(t, strings.Contains(logs.String(), "2 inline findings published; 1 summary-only findings; coverage complete"), logs.String())
	assert.NilError(t, Run(context.Background(), opts))
	assert.Equal(t, len(f.Calls("POST "+reviewsPath)), 1, "reposting one workspace must not duplicate inline findings")
}

func TestVerifiedPublicationRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name    string
		change  func(map[string]string)
		head    string
		baseRef string
		baseSHA string
	}{
		{name: "head changed", head: "different"},
		{name: "base commit changed", baseSHA: "different"},
		{name: "retargeted to branch at same commit", baseRef: "release"},
		{name: "missing base provenance", change: func(files map[string]string) {
			files[review.FileStatus] = strings.Replace(files[review.FileStatus], `"base_ref":"main"`, `"base_ref":""`, 1)
		}},
		{name: "missing provenance", change: func(files map[string]string) { delete(files, review.FileStatus) }},
		{name: "changed output", change: func(files map[string]string) { files[artifact.FileReview] += " " }},
		{name: "removed verified flag", change: func(files map[string]string) {
			files[artifact.FileReview] = strings.Replace(files[artifact.FileReview], `"verified":true`, `"verified":false`, 1)
		}},
		{name: "invalid anchor map", change: func(files map[string]string) { files[artifact.FileValidLines] = `{}` }},
		{name: "wrong workspace SHA", change: func(files map[string]string) { files[artifact.FileHeadSHA] = "different" }},
		{name: "failed marker with findings", change: func(files map[string]string) { files[artifact.FileFailed] = "" }},
		{name: "summary mode with findings", change: func(files map[string]string) { files[artifact.FileMode] = "summary" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, gh := ghtest.New(t)
			happy(f)
			head := tt.head
			if head == "" {
				head = "abc123"
			}
			baseRef, baseSHA := tt.baseRef, tt.baseSHA
			if baseRef == "" {
				baseRef = "main"
			}
			if baseSHA == "" {
				baseSHA = "def456"
			}
			f.JSON("GET /repos/owner/repo/pulls/1", `{"head":{"sha":"`+head+`"},"base":{"ref":"`+baseRef+`","sha":"`+baseSHA+`"}}`)
			files := verifiedArtifacts(t)
			if tt.change != nil {
				tt.change(files)
			}
			ws := workspace(t, files)
			err := Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh})
			assert.Assert(t, err != nil)
			assert.Assert(t, !f.Called("POST "+commentsPath))
			assert.Assert(t, !f.Called("POST "+reviewsPath))
			assert.Assert(t, !f.Called("POST "+labelsPath))
		})
	}
}

func TestVerifiedInlineFailureIsExplicit(t *testing.T) {
	f, gh := ghtest.New(t)
	happy(f)
	f.JSON("GET /repos/owner/repo/pulls/1", `{"head":{"sha":"abc123"},"base":{"ref":"main","sha":"def456"}}`)
	f.Status("POST "+reviewsPath, http.StatusUnprocessableEntity, `{}`)
	ws := workspace(t, verifiedArtifacts(t))
	err := Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh})
	assert.ErrorContains(t, err, "inline publication failed")
	assert.Assert(t, !f.Called("POST "+labelsPath))
	comments := f.Calls("POST " + commentsPath)
	assert.Equal(t, len(comments), 2)
	assert.Assert(t, strings.Contains(comments[1].Body, "not confirmed posted"))
}
