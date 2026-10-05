package ghclient_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"gotest.tools/v3/assert"
)

func TestPullRequestComparisonMetadata(t *testing.T) {
	f, c := ghtest.New(t)
	f.JSON("GET /repos/owner/repo/pulls/1", `{"head":{"sha":"a123","repo":{"full_name":"fork/repo"}},"base":{"ref":"main","sha":"b123"}}`)
	refs, err := c.PullRequestMetadata(context.Background(), ghclient.Repo{Owner: "owner", Name: "repo"}, 1)
	assert.NilError(t, err)
	assert.DeepEqual(t, refs, ghclient.PullRequestMetadata{HeadSHA: "a123", TargetBaseSHA: "b123", BaseRef: "main", HeadRepo: "fork/repo"})
}

func TestMergeBaseUsesImmutableComparison(t *testing.T) {
	tests := []struct{ name, body, wantErr string }{
		{name: "fork network comparison", body: `{"base_commit":{"sha":"b123"},"merge_base_commit":{"sha":"c123"}}`},
		{name: "missing merge base", body: `{"base_commit":{"sha":"b123"}}`, wantErr: "merge base is unavailable"},
		{name: "wrong base", body: `{"base_commit":{"sha":"other"},"merge_base_commit":{"sha":"c123"}}`, wantErr: "base does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := ghtest.New(t)
			f.Handle("GET /repos/owner/repo/compare/b123...a123", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.URL.Query().Get("per_page"), "1")
				_, _ = w.Write([]byte(tt.body))
			})
			sha, err := c.MergeBase(context.Background(), ghclient.Repo{Owner: "owner", Name: "repo"}, "b123", "a123")
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, sha, "c123")
		})
	}
}
