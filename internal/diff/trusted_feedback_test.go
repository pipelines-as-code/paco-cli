package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"gotest.tools/v3/assert"
)

func TestCollectTrustedFeedbackArtifact(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	f.json("GET /repos/owner/repo/collaborators/alice/permission", `{"permission":"write"}`)
	f.json("GET /repos/owner/repo/collaborators/eve/permission", `{"permission":"read"}`)
	f.json("POST /graphql", fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"isResolved":false,"comments":{"nodes":[
			{"path":"a.go","line":2,"body":%q,"author":{"login":"alice"},"pullRequestReview":{"state":"COMMENTED"}},
			{"path":"a.go","line":2,"body":"distinct second issue","author":{"login":"alice"},"pullRequestReview":{"state":"COMMENTED"}},
			{"path":"a.go","line":2,"body":"dismissed","author":{"login":"alice"},"pullRequestReview":{"state":"DISMISSED"}},
			{"path":"a.go","line":2,"body":"untrusted","author":{"login":"eve"},"pullRequestReview":{"state":"COMMENTED"}},
			{"path":"a.go","line":2,"body":"<!-- paco-review -->","author":{"login":"alice"},"pullRequestReview":{"state":"COMMENTED"}}
		]}},
		{"isResolved":true,"comments":{"nodes":[
			{"path":"a.go","line":2,"body":"resolved","author":{"login":"alice"},"pullRequestReview":{"state":"COMMENTED"}}
		]}}
	]}}}}}`, "first issue "+ghtest.Token))
	ws := &artifact.Workspace{Dir: t.TempDir()}
	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
	data, err := ws.Read(artifact.FileExistingFeedbackJSON)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(data), ghtest.Token))
	var feedback artifact.TrustedFeedback
	assert.NilError(t, json.Unmarshal(data, &feedback))
	assert.Equal(t, feedback.Version, 1)
	assert.Equal(t, feedback.Status, "available")
	assert.Assert(t, !feedback.Truncated)
	assert.DeepEqual(t, feedback.Comments, []artifact.TrustedComment{
		{ID: "inline-1", Path: "a.go", Line: 2, Body: "first issue [REDACTED]"},
		{ID: "inline-2", Path: "a.go", Line: 2, Body: "distinct second issue"},
	})
}

func TestTrustedFeedbackBounds(t *testing.T) {
	tests := []struct{ name, body string }{
		{name: "body bound", body: strings.Repeat("a", 2500)},
		{name: "UTF8 truncation", body: strings.Repeat("\u20ac", 800)},
		{name: "encoded bound", body: strings.Repeat("\x01", 1900)},
		{name: "comment count", body: "issue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var comments []ghclient.ThreadComment
			for range 120 {
				comments = append(comments, ghclient.ThreadComment{
					Login: "alice", Path: "a.go", Line: 1, Body: tt.body, ReviewState: "COMMENTED",
				})
			}
			feedback, err := buildTrustedFeedback(comments, map[string]string{"alice": "write"})
			assert.NilError(t, err)
			assert.Equal(t, feedback.Status, "partial")
			assert.Assert(t, feedback.Truncated)
			assert.Assert(t, len(feedback.Comments) > 0 && len(feedback.Comments) <= 100)
			for _, comment := range feedback.Comments {
				assert.Assert(t, len(comment.Body) <= 2000)
				assert.Assert(t, utf8.ValidString(comment.Body))
			}
			data, err := json.Marshal(feedback)
			assert.NilError(t, err)
			assert.Assert(t, len(data) <= maxFeedbackBytes, len(data))
		})
	}
}

func TestTrustedFeedbackReportsMissingCoverage(t *testing.T) {
	tests := []struct{ name, graph, failedRoute, wantStatus string }{
		{name: "missing threads", graph: `{"errors":[{"message":"unavailable"}]}`, wantStatus: "unavailable"},
		{name: "missing reviews", failedRoute: "GET /repos/owner/repo/pulls/1/reviews", wantStatus: "partial"},
		{name: "missing issue comments", failedRoute: "GET /repos/owner/repo/issues/1/comments", wantStatus: "partial"},
		{
			name: "permission lookup failed",
			graph: `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
				{"isResolved":false,"comments":{"nodes":[{"path":"a.go","line":2,"body":"unknown trust","author":{"login":"alice"}}]}}
			]}}}}}`,
			wantStatus: "partial",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeGitHub(t)
			happyPath(f, simpleDiff)
			if tt.graph != "" {
				f.json("POST /graphql", tt.graph)
			}
			if tt.failedRoute != "" {
				f.Handle(tt.failedRoute, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				})
			}
			ws := &artifact.Workspace{Dir: t.TempDir()}
			assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
			data, err := ws.Read(artifact.FileExistingFeedbackJSON)
			assert.NilError(t, err)
			var feedback artifact.TrustedFeedback
			assert.NilError(t, json.Unmarshal(data, &feedback))
			assert.Equal(t, feedback.Status, tt.wantStatus)
			assert.Equal(t, len(feedback.Comments), 0)
		})
	}
}

func TestSkipClearsTrustedFeedback(t *testing.T) {
	ws := &artifact.Workspace{Dir: t.TempDir()}
	assert.NilError(t, ws.Write(artifact.FileExistingFeedbackJSON, []byte("stale")))
	assert.NilError(t, Run(context.Background(), Options{Repo: "invalid", Workspace: ws.Dir}))
	assert.Assert(t, !ws.Exists(artifact.FileExistingFeedbackJSON))
}
