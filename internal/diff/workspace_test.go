package diff

import (
	"context"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"gotest.tools/v3/assert"
)

func TestCollectionClearsReusedWorkspace(t *testing.T) {
	tests := []struct {
		name    string
		success bool
	}{
		{name: "success clears previous error", success: true},
		{name: "early skip clears previous review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &artifact.Workspace{Dir: t.TempDir()}
			removed := []string{
				artifact.FileSource, artifact.FileSourceBefore, artifact.FileReviewRules, artifact.FileToolchains,
				artifact.FileReview, artifact.FileMode, artifact.FileFailed, artifact.FileSecurityBlock, artifact.FileStatus,
			}
			replaced := []string{
				artifact.FileDiff, artifact.FileValidLines, artifact.FileExistingInline, artifact.FileExistingFeedback,
				artifact.FileInputManifest, artifact.FileExistingFeedbackJSON, artifact.FileHeadSHA, artifact.FileError,
			}
			for _, names := range [][]string{removed, replaced, {"unrelated.txt"}} {
				for _, name := range names {
					assert.NilError(t, ws.Write(name, []byte("stale sentinel")))
				}
			}
			f, c := newFakeGitHub(t)
			if tt.success {
				happyPath(f, simpleDiff)
			}
			assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
			for _, name := range removed {
				assert.Assert(t, !ws.Exists(name), name)
			}
			for _, name := range replaced {
				if !ws.Exists(name) {
					continue
				}
				data, err := ws.Read(name)
				assert.NilError(t, err)
				assert.Assert(t, !strings.Contains(string(data), "stale sentinel"), name)
			}
			unrelated, err := ws.Read("unrelated.txt")
			assert.NilError(t, err)
			assert.Equal(t, string(unrelated), "stale sentinel")
			if tt.success {
				assert.Assert(t, !ws.Exists(artifact.FileError))
				assert.Assert(t, ws.Exists(artifact.FileInputManifest))
				diff, err := ws.Read(artifact.FileDiff)
				assert.NilError(t, err)
				assert.Equal(t, string(diff), simpleDiff)
			} else {
				assert.Assert(t, strings.Contains(readSkip(t, ws.Dir), "could not access"))
				assert.Assert(t, !ws.Exists(artifact.FileInputManifest))
				assert.Assert(t, !ws.Exists(artifact.FileExistingFeedbackJSON))
				assert.Assert(t, !ws.Exists(artifact.FileHeadSHA))
				diff, err := ws.Read(artifact.FileDiff)
				assert.NilError(t, err)
				assert.Equal(t, len(diff), 0)
			}
		})
	}
}
