package diff

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"gotest.tools/v3/assert"
)

func serveContextArchive(t *testing.T, f *fakeGitHub, repo, sha string, files map[string]string) {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		assert.NilError(t, tw.WriteHeader(&tar.Header{Name: "root/" + name, Mode: 0o600, Size: int64(len(content))}))
		_, err := tw.Write([]byte(content))
		assert.NilError(t, err)
	}
	assert.NilError(t, tw.Close())
	assert.NilError(t, gz.Close())
	f.Handle("GET /repos/"+repo+"/tarball/"+sha, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", fmt.Sprintf("http://%s/archives/%s", r.Host, sha))
		w.WriteHeader(http.StatusFound)
	})
	f.Handle("GET /archives/"+sha, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data.Bytes())
	})
}

func contextRefs(f *fakeGitHub, diff string) {
	f.Handle("GET /repos/owner/repo/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "diff") {
			_, _ = io.WriteString(w, diff)
			return
		}
		_, _ = io.WriteString(w, `{"head":{"sha":"abc123","repo":{"full_name":"fork/repo"}},"base":{"ref":"main","sha":"def456"}}`)
	})
	f.json("GET /repos/owner/repo/compare/def456...abc123", `{"base_commit":{"sha":"def456"},"merge_base_commit":{"sha":"aaa111"}}`)
}

func readManifest(t *testing.T, ws *artifact.Workspace) artifact.InputManifest {
	t.Helper()
	data, err := ws.Read(artifact.FileInputManifest)
	assert.NilError(t, err)
	var manifest artifact.InputManifest
	assert.NilError(t, json.Unmarshal(data, &manifest))
	return manifest
}

func TestCollectPinnedComparison(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveContextArchive(t, f, "fork/repo", "abc123", map[string]string{"a.go": "package a\nvar x = 1\n", ".env": "excluded"})
	serveContextArchive(t, f, "owner/repo", "aaa111", map[string]string{"a.go": "package a\n"})
	f.file(".tekton/ai/REVIEW.md", "def456", "trusted rules "+ghtest.Token)
	f.Handle("GET /repos/owner/repo/contents/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Query().Get("ref"), "def456")
		_, _ = io.WriteString(w, `[{"type":"file","name":"go.mod"}]`)
	})
	f.file("go.mod", "def456", "module example.test\n\ngo 1.26\n")
	ws := &artifact.Workspace{Dir: t.TempDir()}
	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
	manifest := readManifest(t, ws)
	assert.Equal(t, manifest.Version, 1)
	assert.Equal(t, manifest.Repo, "owner/repo")
	assert.Equal(t, manifest.PRNumber, 1)
	assert.Equal(t, manifest.HeadSHA, "abc123")
	assert.Equal(t, manifest.BaseRef, "main")
	assert.Equal(t, manifest.TargetBaseSHA, "def456")
	assert.Equal(t, manifest.MergeBaseSHA, "aaa111")
	assert.Equal(t, manifest.DiffDigest, fmt.Sprintf("%x", sha256.Sum256([]byte(simpleDiff))))
	assert.Equal(t, manifest.ContextStatus, "partial")
	assert.Equal(t, manifest.Head.Status, "partial")
	assert.Equal(t, manifest.Head.Excluded, 1)
	assert.Equal(t, manifest.Before.Status, "available")
	headData, err := ws.Read(artifact.FileSource)
	assert.NilError(t, err)
	head, err := source.Decode(headData, "abc123")
	assert.NilError(t, err)
	beforeData, err := ws.Read(artifact.FileSourceBefore)
	assert.NilError(t, err)
	before, err := source.Decode(beforeData, "aaa111")
	assert.NilError(t, err)
	assert.NilError(t, source.ValidateCombined(head, before))
	assert.Equal(t, before.Files["a.go"], "package a\n")
	rules, err := ws.Read(artifact.FileReviewRules)
	assert.NilError(t, err)
	assert.Equal(t, string(rules), "trusted rules [REDACTED]")
	assert.Assert(t, f.called("GET /repos/fork/repo/tarball/abc123"))
	assert.Assert(t, !f.called("GET /repos/owner/repo/tarball/def456"))
	assert.Assert(t, ws.Exists(artifact.FileToolchains))
}

func TestCollectionRejectsMovingRefs(t *testing.T) {
	tests := []struct{ name, after string }{
		{name: "head", after: `{"head":{"sha":"changed"},"base":{"ref":"main","sha":"def456"}}`},
		{name: "base commit", after: `{"head":{"sha":"abc123"},"base":{"ref":"main","sha":"changed"}}`},
		{name: "base branch", after: `{"head":{"sha":"abc123"},"base":{"ref":"other","sha":"def456"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeGitHub(t)
			happyPath(f, simpleDiff)
			calls := 0
			f.Handle("GET /repos/owner/repo/pulls/1", func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.Header.Get("Accept"), "diff") {
					_, _ = io.WriteString(w, simpleDiff)
					return
				}
				calls++
				if calls == 1 {
					_, _ = io.WriteString(w, `{"head":{"sha":"abc123"},"base":{"ref":"main","sha":"def456"}}`)
				} else {
					_, _ = io.WriteString(w, tt.after)
				}
			})
			ws := &artifact.Workspace{Dir: t.TempDir()}
			for _, name := range []string{artifact.FileInputManifest, artifact.FileSourceBefore, artifact.FileSource} {
				assert.NilError(t, ws.Write(name, []byte("stale")))
			}
			assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
			assert.Assert(t, strings.Contains(readSkip(t, ws.Dir), "changed during"))
			for _, name := range []string{artifact.FileInputManifest, artifact.FileSourceBefore, artifact.FileSource} {
				assert.Assert(t, !ws.Exists(name), name)
			}
			data, err := ws.Read(artifact.FileDiff)
			assert.NilError(t, err)
			assert.Equal(t, len(data), 0)
		})
	}
}

func TestCollectionReportsUnavailableBefore(t *testing.T) {
	tests := []struct {
		name         string
		compare      string
		wantMergeSHA string
		wantReason   string
	}{
		{name: "missing base SHA", wantReason: "Target-base SHA"},
		{name: "merge base unavailable", compare: "missing", wantReason: "merge base is unavailable"},
		{name: "before archive unavailable", compare: "available", wantMergeSHA: "aaa111", wantReason: "fetching source archive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeGitHub(t)
			happyPath(f, simpleDiff)
			if tt.compare != "" {
				contextRefs(f, simpleDiff)
				if tt.compare == "missing" {
					f.json("GET /repos/owner/repo/compare/def456...abc123", `{"base_commit":{"sha":"def456"}}`)
				}
			}
			serveContextArchive(t, f, "owner/repo", "abc123", map[string]string{"a.go": "package a\nvar x = 1\n"})
			ws := &artifact.Workspace{Dir: t.TempDir()}
			assert.NilError(t, ws.Write(artifact.FileSourceBefore, []byte("stale")))
			assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
			assert.Assert(t, !ws.Exists(artifact.FileError))
			manifest := readManifest(t, ws)
			assert.Equal(t, manifest.ContextStatus, "partial")
			assert.Equal(t, manifest.Head.Status, "available")
			assert.Equal(t, manifest.Before.Status, "unavailable")
			assert.Equal(t, manifest.MergeBaseSHA, tt.wantMergeSHA)
			assert.Assert(t, strings.Contains(manifest.Before.Reason, tt.wantReason), manifest.Before.Reason)
			assert.Assert(t, !ws.Exists(artifact.FileSourceBefore))
		})
	}
}

func TestCollectionScrubsContextAndDigest(t *testing.T) {
	f, c := newFakeGitHub(t)
	diff := strings.ReplaceAll(simpleDiff, "var x = 1", "var token = \""+ghtest.Token+"\"")
	happyPath(f, diff)
	contextRefs(f, diff)
	serveContextArchive(t, f, "owner/repo", "abc123", map[string]string{"a.go": ghtest.Token})
	serveContextArchive(t, f, "owner/repo", "aaa111", map[string]string{"a.go": ghtest.Token})
	ws := &artifact.Workspace{Dir: t.TempDir()}
	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
	for _, name := range []string{artifact.FileDiff, artifact.FileSource, artifact.FileSourceBefore, artifact.FileInputManifest} {
		data, err := ws.Read(name)
		assert.NilError(t, err)
		assert.Assert(t, !strings.Contains(string(data), ghtest.Token), name)
	}
	diffData, err := ws.Read(artifact.FileDiff)
	assert.NilError(t, err)
	assert.Equal(t, readManifest(t, ws).DiffDigest, fmt.Sprintf("%x", sha256.Sum256(diffData)))
}

func TestCombinedCollectionKeepsHeadWhenBeforeExceedsLimit(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	files := map[string]string{}
	for i := range 17 {
		files[fmt.Sprintf("%d.txt", i)] = strings.Repeat("x", 512<<10)
	}
	serveContextArchive(t, f, "owner/repo", "abc123", files)
	serveContextArchive(t, f, "owner/repo", "aaa111", files)
	ws := &artifact.Workspace{Dir: t.TempDir()}
	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
	manifest := readManifest(t, ws)
	assert.Equal(t, manifest.Head.Status, "available")
	assert.Equal(t, manifest.Before.Status, "unavailable")
	assert.Assert(t, strings.Contains(manifest.Before.Reason, "combined source snapshots"))
	assert.Assert(t, ws.Exists(artifact.FileSource))
	_, err := os.Stat(ws.Path(artifact.FileSourceBefore))
	assert.Assert(t, os.IsNotExist(err))
}
