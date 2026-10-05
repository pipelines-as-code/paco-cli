package post

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"gotest.tools/v3/assert"
)

const (
	commentsPath = "/repos/owner/repo/issues/1/comments"
	reviewsPath  = "/repos/owner/repo/pulls/1/reviews"
	labelsPath   = "/repos/owner/repo/labels"
	issueLabels  = "/repos/owner/repo/issues/1/labels"
)

func workspace(t *testing.T, files map[string]string) string {
	t.Helper()
	ws := t.TempDir()
	for name, content := range files {
		assert.NilError(t, os.WriteFile(filepath.Join(ws, name), []byte(content), 0o600))
	}
	return ws
}

func reviewJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	assert.NilError(t, err)
	return string(b)
}

func bodyOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	assert.NilError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

func happy(f *ghtest.Fake) {
	f.JSON("GET "+commentsPath, `[]`)
	f.Status("POST "+commentsPath, http.StatusCreated, `{"id":9}`)
	f.Status("POST "+labelsPath, http.StatusCreated, `{}`)
	f.JSON("POST "+issueLabels, `[]`)
	f.Status("POST "+reviewsPath, http.StatusOK, `{"id":1}`)
}

func defaultArtifacts(t *testing.T, rev map[string]any) map[string]string {
	t.Helper()
	return map[string]string{
		artifact.FileReview:         reviewJSON(t, rev),
		artifact.FileValidLines:     `{"a.go":{"10":true,"20":true}}`,
		artifact.FileExistingInline: `{"a.go":{"20":true}}`,
		artifact.FileHeadSHA:        "abc123\n",
		artifact.FileMode:           "review\n",
	}
}

func TestRunPostsReview(t *testing.T) {
	f, gh := ghtest.New(t)
	happy(f)
	ws := workspace(t, defaultArtifacts(t, map[string]any{
		"summary":            "Looks fine.",
		"review_score":       map[string]any{"rating": 2, "reason": "small"},
		"security_sensitive": false,
		"comments": []map[string]any{
			{"path": "a.go", "line": 10, "severity": "high", "body": "bug"},
			{"path": "a.go", "line": 20, "severity": "low", "body": "already posted"},
			{"path": "a.go", "line": 99, "severity": "low", "body": "outside diff"},
		},
	}))

	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh}))

	created := f.Calls("POST " + commentsPath)
	assert.Equal(t, len(created), 1)
	assert.Equal(t, bodyOf(t, created[0].Body)["body"],
		marker+"\n## Paco Review \U0001F50D\n\nLooks fine.\n\n**Review difficulty:** 2/5 (Easy): small\n\n\n1 new inline comment(s) found.\n\n<sub>Reviewed commit: abc123</sub>")

	reviews := f.Calls("POST " + reviewsPath)
	assert.Equal(t, len(reviews), 1)
	assert.DeepEqual(t, bodyOf(t, reviews[0].Body), map[string]any{
		"commit_id": "abc123",
		"body":      "Paco inline comments -- see the Paco Review summary comment for the overview.",
		"event":     "COMMENT",
		"comments": []any{map[string]any{
			"path": "a.go", "line": float64(10), "side": "RIGHT", "body": "**[HIGH]** bug",
		}},
	})

	labels := f.Calls("POST " + labelsPath)
	assert.Equal(t, len(labels), 1)
	assert.DeepEqual(t, bodyOf(t, labels[0].Body), map[string]any{
		"name": "paco/review-easy", "color": "5be3a0", "description": "Paco review difficulty",
	})
	added := f.Calls("POST " + issueLabels)
	assert.Equal(t, len(added), 1)
	assert.Equal(t, strings.TrimSpace(added[0].Body), `["paco/review-easy"]`)

	for _, sl := range scoreLabels {
		removed := f.Called("DELETE " + issueLabels + "/" + sl.Name)
		assert.Equal(t, removed, sl.Name != "paco/review-easy", sl.Name)
	}
	assert.Assert(t, !f.Called("DELETE "+issueLabels+"/security-review"))
}

func TestRunUpdatesStickyOnLaterPage(t *testing.T) {
	f, gh := ghtest.New(t)
	happy(f)
	f.Handle("GET "+commentsPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprintf(w, `[{"id":77,"body":"old\n%s"}]`, marker)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, commentsPath))
		_, _ = w.Write([]byte(`[{"id":5,"body":"unrelated"}]`))
	})
	f.JSON("PATCH /repos/owner/repo/issues/comments/77", `{"id":77}`)
	ws := workspace(t, defaultArtifacts(t, map[string]any{"summary": "s", "comments": []any{}}))

	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh}))

	assert.Assert(t, f.Called("PATCH /repos/owner/repo/issues/comments/77"))
	assert.Assert(t, !f.Called("POST "+commentsPath))
	assert.Assert(t, !f.Called("POST "+reviewsPath))
}

func TestRunStickyFailure(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *ghtest.Fake)
		want  string
	}{
		{
			name:  "list fails",
			setup: func(f *ghtest.Fake) { f.Status("GET "+commentsPath, http.StatusInternalServerError, `{}`) },
			want:  "listing pull request comments",
		},
		{
			name: "create fails",
			setup: func(f *ghtest.Fake) {
				f.JSON("GET "+commentsPath, `[]`)
				f.Status("POST "+commentsPath, http.StatusForbidden, `{"message":"nope"}`)
			},
			want: "creating the Paco summary comment",
		},
		{
			name: "update fails",
			setup: func(f *ghtest.Fake) {
				f.JSON("GET "+commentsPath, fmt.Sprintf(`[{"id":3,"body":%q}]`, marker))
				f.Status("PATCH /repos/owner/repo/issues/comments/3", http.StatusForbidden, `{}`)
			},
			want: "updating the Paco summary comment",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, gh := ghtest.New(t)
			tt.setup(f)
			ws := workspace(t, defaultArtifacts(t, map[string]any{"summary": "s"}))
			err := Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh})
			assert.ErrorContains(t, err, tt.want)
			assert.Assert(t, !f.Called("POST "+labelsPath))
		})
	}
}

func TestRunLabels(t *testing.T) {
	tests := []struct {
		name          string
		rev           map[string]any
		failed        bool
		setup         func(f *ghtest.Fake)
		wantLabels    []string
		wantNoLabels  bool
		wantSecLabel  bool
		wantReconcile bool
	}{
		{
			name:       "out of range rating clamps to moderate",
			rev:        map[string]any{"summary": "s", "review_score": map[string]any{"rating": 9}},
			wantLabels: []string{`["paco/review-moderate"]`},
		},
		{
			name:         "security sensitive adds security-review",
			rev:          map[string]any{"summary": "s", "review_score": map[string]any{"rating": 5}, "security_sensitive": true},
			wantLabels:   []string{`["paco/review-very-hard"]`, `["security-review"]`},
			wantSecLabel: true,
		},
		{
			name: "existing label is reconciled",
			rev:  map[string]any{"summary": "s", "review_score": map[string]any{"rating": 1}},
			setup: func(f *ghtest.Fake) {
				f.Status("POST "+labelsPath, http.StatusUnprocessableEntity,
					`{"message":"Validation Failed","errors":[{"resource":"Label","code":"already_exists","field":"name"}]}`)
				f.JSON("PATCH "+labelsPath+"/paco/review-trivial", `{}`)
			},
			wantLabels:    []string{`["paco/review-trivial"]`},
			wantReconcile: true,
		},
		{
			name: "label errors are not fatal",
			rev:  map[string]any{"summary": "s", "review_score": map[string]any{"rating": 4}},
			setup: func(f *ghtest.Fake) {
				f.Status("POST "+labelsPath, http.StatusInternalServerError, `{}`)
				f.Status("POST "+issueLabels, http.StatusInternalServerError, `{}`)
				for _, sl := range scoreLabels {
					f.Status("DELETE "+issueLabels+"/"+sl.Name, http.StatusInternalServerError, `{}`)
				}
			},
			wantLabels: []string{`["paco/review-hard"]`},
		},
		{
			name:         "failed run applies no labels",
			rev:          map[string]any{"summary": "s", "review_score": map[string]any{"rating": 2}},
			failed:       true,
			wantNoLabels: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, gh := ghtest.New(t)
			happy(f)
			if tt.setup != nil {
				tt.setup(f)
			}
			files := defaultArtifacts(t, tt.rev)
			if tt.failed {
				files[artifact.FileFailed] = ""
			}
			ws := workspace(t, files)

			assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh}))

			if tt.wantNoLabels {
				assert.Equal(t, len(f.Calls("POST "+labelsPath)), 0)
				assert.Equal(t, len(f.Calls("POST "+issueLabels)), 0)
				for _, c := range f.Calls("") {
					assert.Assert(t, !strings.HasPrefix(c.Key, "DELETE "), c.Key)
				}
				return
			}
			var got []string
			for _, c := range f.Calls("POST " + issueLabels) {
				got = append(got, strings.TrimSpace(c.Body))
			}
			assert.DeepEqual(t, got, tt.wantLabels)
			assert.Equal(t, f.Called("PATCH "+labelsPath+"/paco/review-trivial"), tt.wantReconcile)

			var secCreated bool
			for _, c := range f.Calls("POST " + labelsPath) {
				if bodyOf(t, c.Body)["name"] == "security-review" {
					secCreated = true
					assert.DeepEqual(t, bodyOf(t, c.Body), map[string]any{
						"name": "security-review", "color": "b60205", "description": "Flagged as security-sensitive by Paco",
					})
				}
			}
			assert.Equal(t, secCreated, tt.wantSecLabel)
			assert.Assert(t, !f.Called("DELETE "+issueLabels+"/security-review"))
		})
	}
}

func TestRunInlineFailureAddsNote(t *testing.T) {
	f, gh := ghtest.New(t)
	happy(f)
	f.Status("POST "+reviewsPath, http.StatusUnprocessableEntity, `{"message":"line must be part of the diff"}`)
	f.JSON("GET "+commentsPath, `[]`)
	ws := workspace(t, defaultArtifacts(t, map[string]any{
		"summary":  "s",
		"comments": []map[string]any{{"path": "a.go", "line": 10, "severity": "low", "body": "x"}},
	}))

	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh}))

	posts := f.Calls("POST " + commentsPath)
	assert.Equal(t, len(posts), 2)
	first := bodyOf(t, posts[0].Body)["body"].(string)
	second := bodyOf(t, posts[1].Body)["body"].(string)
	assert.Equal(t, second, first+"\n\n> [!NOTE]\n> Some inline comments could not be posted (a line number may fall outside the diff).")
}

func TestRunWithholds(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "security block artifact",
			files: map[string]string{
				artifact.FileReview:        `{"summary":"s"}`,
				artifact.FileSecurityBlock: "",
			},
		},
		{
			name:  "review contains the GitHub token",
			files: map[string]string{artifact.FileReview: fmt.Sprintf(`{"summary":"leak %s"}`, ghtest.Token)},
		},
		{
			name:  "review contains a credential pattern",
			files: map[string]string{artifact.FileReview: `{"summary":"ghp_` + strings.Repeat("a", 36) + `"}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, gh := ghtest.New(t)
			happy(f)
			ws := workspace(t, tt.files)

			assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: gh}))

			posts := f.Calls("POST " + commentsPath)
			assert.Equal(t, len(posts), 1)
			assert.Equal(t, bodyOf(t, posts[0].Body)["body"], withheldBody)
			assert.Assert(t, !f.Called("POST "+reviewsPath))
			assert.Assert(t, !f.Called("POST "+labelsPath))
		})
	}
}

func TestRunConfigErrors(t *testing.T) {
	tests := []struct {
		name  string
		repo  string
		files map[string]string
		want  string
	}{
		{name: "missing review artifact", repo: "owner/repo", want: "cannot read review"},
		{name: "invalid repo", repo: "nope", files: map[string]string{artifact.FileReview: `{}`}, want: "invalid repo format: nope"},
		{name: "missing token", repo: "owner/repo", files: map[string]string{artifact.FileReview: `{}`}, want: "no GitHub token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			ws := workspace(t, tt.files)
			err := Run(context.Background(), Options{Repo: tt.repo, PRNumber: 1, Workspace: ws})
			assert.ErrorContains(t, err, tt.want)
		})
	}
}
