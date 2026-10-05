package diff

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"gotest.tools/v3/assert"
)

func TestRunSourceSnapshot(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	content := "package a\n"
	assert.NilError(t, tw.WriteHeader(&tar.Header{Name: "root/a.go", Mode: 0o600, Size: int64(len(content))}))
	_, err := tw.Write([]byte(content))
	assert.NilError(t, err)
	assert.NilError(t, tw.Close())
	assert.NilError(t, gz.Close())
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	f.Handle("GET /repos/owner/repo/tarball/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", fmt.Sprintf("http://%s/archive", r.Host))
		w.WriteHeader(http.StatusFound)
	})
	f.Handle("GET /archive", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive.Bytes())
	})
	ws := t.TempDir()
	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: c}))
	data, err := os.ReadFile(filepath.Join(ws, artifact.FileSource))
	assert.NilError(t, err)
	var snapshot source.Snapshot
	assert.NilError(t, json.Unmarshal(data, &snapshot))
	assert.Equal(t, snapshot.Commit, "abc123")
	assert.Equal(t, snapshot.Files["a.go"], content)
}

func TestRunClearsStaleSource(t *testing.T) {
	for _, skip := range []bool{false, true} {
		ws := t.TempDir()
		assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileSource), []byte("stale"), 0o600))
		f, c := newFakeGitHub(t)
		if !skip {
			happyPath(f, simpleDiff)
		}
		assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws, GitHub: c}))
		_, err := os.Stat(filepath.Join(ws, artifact.FileSource))
		assert.Assert(t, os.IsNotExist(err))
	}
}
