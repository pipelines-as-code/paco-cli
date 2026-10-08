package diff

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"gotest.tools/v3/assert"
)

const simpleDiff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n package a\n+var x = 1\n"

// fakeGitHub adds diff-specific fixtures on top of ghtest.Fake.
type fakeGitHub struct{ *ghtest.Fake }

func newFakeGitHub(t *testing.T) (*fakeGitHub, *ghclient.Client) {
	t.Helper()
	f, c := ghtest.New(t)
	return &fakeGitHub{f}, c
}

func (f *fakeGitHub) json(key, body string) { f.JSON(key, body) }

func (f *fakeGitHub) called(key string) bool { return f.Called(key) }

// pullRequest serves both the JSON and the raw diff representations of a PR.
func (f *fakeGitHub) pullRequest(base, diff string) {
	f.Handle("GET /repos/owner/repo/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "diff") {
			_, _ = io.WriteString(w, diff)
			return
		}
		_, _ = fmt.Fprintf(w, `{"number":1,"head":{"sha":"abc123"},"base":{"ref":%q}}`, base)
	})
}

func (f *fakeGitHub) file(path, ref, content string) {
	f.Handle("GET /repos/owner/repo/contents/"+path, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != ref {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"type":"file","encoding":"base64","name":%q,"path":%q,"content":%q}`,
			filepath.Base(path), path, base64.StdEncoding.EncodeToString([]byte(content)))
	})
}

// happyPath registers everything a successful run touches with no feedback.
func happyPath(f *fakeGitHub, diff string) {
	f.json("GET /repos/owner/repo", `{"id":1}`)
	f.json("POST /repos/owner/repo/issues/1/reactions", `{}`)
	f.pullRequest("main", diff)
	f.json("POST /graphql", `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}}`)
	f.json("GET /repos/owner/repo/pulls/1/reviews", `[]`)
	f.json("GET /repos/owner/repo/issues/1/comments", `[]`)
}

func readSkip(t *testing.T, ws string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ws, artifact.FileError))
	assert.NilError(t, err)
	return strings.TrimSpace(string(data))
}

func TestRunSkips(t *testing.T) {
	tests := []struct {
		name  string
		repo  string
		noGH  bool
		setup func(f *fakeGitHub)
		want  string
	}{
		{
			name: "missing token",
			noGH: true,
			want: "Paco: could not configure GitHub access: no GitHub token: set GH_TOKEN or GITHUB_TOKEN.",
		},
		{
			name: "invalid repo",
			repo: "not-a-repo",
			want: "Paco: invalid repo format: not-a-repo.",
		},
		{
			name:  "token cannot access the repo",
			setup: func(*fakeGitHub) {},
			want:  "Paco: the Pipelines-as-Code GitHub App token could not access owner/repo.",
		},
		{
			name:  "pull request lookup fails",
			setup: func(f *fakeGitHub) { f.json("GET /repos/owner/repo", `{"id":1}`) },
			want:  "Paco: could not look up pull request #1 on owner/repo.",
		},
		{
			name: "pull request refs missing",
			setup: func(f *fakeGitHub) {
				f.json("GET /repos/owner/repo", `{"id":1}`)
				f.json("GET /repos/owner/repo/pulls/1", `{"number":1}`)
			},
			want: "Paco: could not read pull request #1 refs on owner/repo.",
		},
		{
			name: "diff fetch fails",
			setup: func(f *fakeGitHub) {
				f.json("GET /repos/owner/repo", `{"id":1}`)
				f.Handle("GET /repos/owner/repo/pulls/1", func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.Header.Get("Accept"), "diff") {
						w.WriteHeader(http.StatusNotAcceptable)
						return
					}
					_, _ = io.WriteString(w, `{"head":{"sha":"abc123"},"base":{"ref":"main"}}`)
				})
			},
			want: "Paco: could not fetch the diff for pull request #1.",
		},
		{
			name:  "diff too large",
			setup: func(f *fakeGitHub) { happyPath(f, strings.Repeat("x", maxDiffBytes+1)) },
			want:  "Paco: PR diff is too large (200001 bytes, limit is 200000), so review was skipped instead of using a truncated diff.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			ws := t.TempDir()
			opts := Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws}
			if tt.repo != "" {
				opts.Repo = tt.repo
			}
			if !tt.noGH {
				f, c := newFakeGitHub(t)
				if tt.setup != nil {
					tt.setup(f)
				}
				opts.GitHub = c
			}

			assert.NilError(t, Run(context.Background(), opts))
			assert.Equal(t, readSkip(t, ws), tt.want)
		})
	}
}

func TestRunSuccess(t *testing.T) {
	ws := t.TempDir()
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)

	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: c}))

	assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileError)))
	for _, name := range []string{artifact.FileDiff, artifact.FileValidLines, artifact.FileExistingInline, artifact.FileExistingFeedback} {
		assert.Assert(t, fileExists(filepath.Join(ws, name)), name)
	}
	headSHA, _ := os.ReadFile(filepath.Join(ws, artifact.FileHeadSHA))
	assert.Equal(t, string(headSHA), "abc123")
	validLines, _ := os.ReadFile(filepath.Join(ws, artifact.FileValidLines))
	assert.Equal(t, string(validLines), `{"a.go":{"1":true,"2":true}}`)
	assert.Assert(t, f.called("POST /repos/owner/repo/issues/1/reactions"), "eyes reaction on the PR")
}

func TestRunReactsToTriggerComment(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	var content string
	f.Handle("POST /repos/owner/repo/issues/comments/55/reactions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		content = body["content"]
		_, _ = io.WriteString(w, `{}`)
	})

	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, CommentID: "55", Workspace: t.TempDir(), GitHub: c}))
	assert.Equal(t, content, "eyes")
	assert.Assert(t, !f.called("POST /repos/owner/repo/issues/1/reactions"))
}

func TestRunRedactsCredentials(t *testing.T) {
	ws := t.TempDir()
	f, c := newFakeGitHub(t)
	happyPath(f, "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n package a\n+token := \"ghp_ABCDEFghijklmnopqrstuvwx\"\n")

	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: c}))

	diffContent, _ := os.ReadFile(filepath.Join(ws, artifact.FileDiff))
	assert.Assert(t, !strings.Contains(string(diffContent), "ghp_ABCDEFghijklmnopqrstuvwx"), "diff should be redacted")
	assert.Assert(t, strings.Contains(string(diffContent), "[REDACTED]"), "diff should contain [REDACTED]")
}

func TestRunExistingFeedback(t *testing.T) {
	ws := t.TempDir()
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	f.json("POST /graphql", `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"isResolved":false,"comments":{"nodes":[
			{"path":"a.go","line":2,"body":"rename this","author":{"login":"alice"},"pullRequestReview":{"state":"COMMENTED"}},
			{"path":"a.go","line":null,"originalLine":1,"body":"outdated note","author":{"login":"alice"},"pullRequestReview":null},
			{"path":"b.go","line":9,"body":"drive-by","author":{"login":"eve"},"pullRequestReview":{"state":"COMMENTED"}}
		]}},
		{"isResolved":true,"comments":{"nodes":[
			{"path":"c.go","line":3,"body":"done","author":{"login":"alice"},"pullRequestReview":{"state":"COMMENTED"}}
		]}}
	]}}}}}`)
	f.json("GET /repos/owner/repo/pulls/1/reviews", `[
		{"user":{"login":"bob"},"body":"Looks fine\nmostly","state":"APPROVED"},
		{"user":{"login":"bob"},"body":"## Paco Review\nold","state":"COMMENTED"}
	]`)
	f.json("GET /repos/owner/repo/issues/1/comments", `[
		{"id":1,"user":{"login":"alice"},"body":"please add tests"},
		{"id":2,"user":{"login":"paco-bot"},"body":"<!-- paco-review -->\nsticky"}
	]`)
	f.json("GET /repos/owner/repo/collaborators/alice/permission", `{"permission":"write"}`)
	f.json("GET /repos/owner/repo/collaborators/bob/permission", `{"permission":"admin"}`)
	f.json("GET /repos/owner/repo/collaborators/eve/permission", `{"permission":"read"}`)
	// paco-bot's permission lookup 404s and is treated as untrusted.

	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: c}))

	feedback, err := os.ReadFile(filepath.Join(ws, artifact.FileExistingFeedback))
	assert.NilError(t, err)
	assert.Equal(t, string(feedback), strings.Join([]string{
		"- alice on a.go:2: rename this",
		"- alice on a.go:1: outdated note",
		"- review by bob: Looks fine mostly",
		"- comment by alice: please add tests",
	}, "\n"))
	inline, err := os.ReadFile(filepath.Join(ws, artifact.FileExistingInline))
	assert.NilError(t, err)
	assert.Equal(t, string(inline), `{"a.go":{"1":true,"2":true}}`)
}

func TestRunToolchains(t *testing.T) {
	tests := []struct {
		name       string
		baseBranch string
		root       []string
		files      map[string]string
		listErr    bool
		want       string
		rules      string
	}{
		{
			name: "writes versions of files present at the root",
			root: []string{"README.md", "go.mod", "package.json"},
			files: map[string]string{
				"go.mod":       "module example.com/foo\n\ngo 1.27.1\n",
				"package.json": `{"engines":{"node":">=20"}}`,
			},
			want: "Go\t1.27.1\tgo.mod\nNode.js\t>=20\tpackage.json\n",
		},
		{
			name:       "reads toolchains and rules from a non-main base branch",
			baseBranch: "release/1.2",
			root:       []string{"go.mod"},
			files:      map[string]string{"go.mod": "module example.com/foo\n\ngo 1.26\n"},
			want:       "Go\t1.26\tgo.mod\n",
			rules:      "Review release compatibility\n",
		},
		{
			name: "skips when no version file is present",
			root: []string{"README.md", "Makefile"},
		},
		{
			name:    "skips when listing the root fails",
			listErr: true,
		},
		{
			name: "skips a version file that cannot be fetched",
			root: []string{"go.mod"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			workspace := &artifact.Workspace{Dir: ws}
			assert.NilError(t, workspace.Write(artifact.FileToolchains, []byte("Go\t9.99\tgo.mod\n")))
			assert.NilError(t, workspace.Write(artifact.FileReviewRules, []byte("stale rules\n")))
			baseBranch := tt.baseBranch
			if baseBranch == "" {
				baseBranch = "main"
			}

			f, c := newFakeGitHub(t)
			happyPath(f, simpleDiff)
			f.pullRequest(baseBranch, simpleDiff)
			if !tt.listErr {
				f.Handle("GET /repos/owner/repo/contents/", func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, r.URL.Query().Get("ref"), baseBranch)
					entries := []map[string]string{{"type": "dir", "name": "docs"}}
					for _, name := range tt.root {
						entries = append(entries, map[string]string{"type": "file", "name": name})
					}
					_ = json.NewEncoder(w).Encode(entries)
				})
			}
			for name, content := range tt.files {
				f.file(name, baseBranch, content)
			}
			if tt.rules != "" {
				f.file(".tekton/ai/REVIEW.md", baseBranch, tt.rules)
			}

			assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: c}))

			got, readErr := os.ReadFile(filepath.Join(ws, artifact.FileToolchains))
			if tt.want == "" {
				assert.Assert(t, os.IsNotExist(readErr), "expected no %s artifact", artifact.FileToolchains)
			} else {
				assert.NilError(t, readErr)
				assert.Equal(t, tt.want, string(got))
			}
			rules, rulesErr := os.ReadFile(filepath.Join(ws, artifact.FileReviewRules))
			if tt.rules != "" {
				assert.NilError(t, rulesErr)
				assert.Equal(t, tt.rules, string(rules))
			} else {
				assert.Assert(t, os.IsNotExist(rulesErr), "expected no %s artifact", artifact.FileReviewRules)
			}
		})
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
