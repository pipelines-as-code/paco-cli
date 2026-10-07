// Package reviewbench runs paco under the ReviewBench agent contract: it
// builds review inputs from a mounted checkout and diff instead of the GitHub
// API, and converts the resulting review into a findings file.
package reviewbench

import (
	"encoding/json"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/pipelines-as-code/paco-cli/internal/source"
)

type Input struct {
	Repo     string // owner/name
	PRNumber int
	BaseSHA  string
	HeadSHA  string
	RepoDir  string // working tree checked out at HeadSHA
	Diff     string // three-dot diff from BaseSHA to HeadSHA
}

// Prepare writes the artifacts `paco diff` would produce. There is no prior
// feedback, and base-branch rules and toolchains are not collected. The old
// side of the diff is rebuilt from the checkout, so its snapshot is labelled
// with BaseSHA. A non-empty skip reason means paco would not review the change.
func Prepare(ws *artifact.Workspace, in Input) (skip string, parsed *diff.ParsedDiff, err error) {
	if strings.TrimSpace(in.Diff) == "" {
		return "No reviewable changes found in this diff.", nil, nil
	}
	// Same pattern redaction `paco diff` applies; snapshots get it too.
	redacted := security.Scrub(in.Diff)
	parsed, err = diff.Parse(redacted)
	if err != nil {
		return "Paco: could not use the pull request diff: " + err.Error() + ".", nil, nil
	}
	validLines, err := json.Marshal(diff.ValidLines(parsed))
	if err != nil {
		return "", nil, err
	}
	feedback, err := json.Marshal(artifact.TrustedFeedback{Version: 1, Status: "available", Comments: []artifact.TrustedComment{}})
	if err != nil {
		return "", nil, err
	}
	for name, data := range map[string][]byte{
		artifact.FileDiff:                 []byte(redacted),
		artifact.FileHeadSHA:              []byte(in.HeadSHA),
		artifact.FileValidLines:           validLines,
		artifact.FileExistingInline:       []byte("{}"),
		artifact.FileExistingFeedback:     nil,
		artifact.FileExistingFeedbackJSON: feedback,
	} {
		if err := ws.Write(name, data); err != nil {
			return "", nil, err
		}
	}

	manifest := artifact.InputManifest{
		Version: 1, Repo: in.Repo, PRNumber: in.PRNumber, HeadSHA: in.HeadSHA,
		BaseRef: in.BaseSHA, TargetBaseSHA: in.BaseSHA, MergeBaseSHA: in.BaseSHA,
		DiffDigest: review.Digest([]byte(redacted)), ContextStatus: "complete",
		Head:        artifact.ContextState{Status: "unavailable"},
		Before:      artifact.ContextState{Status: "unavailable"},
		Limitations: diff.Limitations(parsed),
	}
	head, err := source.FromDir(in.RepoDir, in.HeadSHA)
	var before *source.Snapshot
	if err != nil {
		manifest.Head.Reason = err.Error()
		manifest.Before.Reason = "Head context is unavailable."
	} else {
		before = source.Reverse(head, parsed, in.BaseSHA)
		if source.ValidateCombined(head, before) != nil {
			// A full second copy does not fit; keep only the files the diff touches.
			before = changedOnly(before, parsed)
			if err := source.ValidateCombined(head, before); err != nil {
				before = nil
				manifest.Before.Reason = err.Error()
			}
		}
	}
	for _, item := range []struct {
		snapshot *source.Snapshot
		name     string
		state    *artifact.ContextState
	}{
		{head, artifact.FileSource, &manifest.Head},
		{before, artifact.FileSourceBefore, &manifest.Before},
	} {
		if item.snapshot == nil {
			manifest.ContextStatus = "partial"
			continue
		}
		item.state.Status, item.state.Excluded = "available", item.snapshot.Excluded
		if item.snapshot.Excluded > 0 {
			item.state.Status = "partial"
			manifest.ContextStatus = "partial"
		}
		encoded, err := json.Marshal(item.snapshot)
		if err != nil {
			return "", nil, err
		}
		if err := ws.Write(item.name, encoded); err != nil {
			return "", nil, err
		}
	}
	if len(manifest.Limitations) > 0 {
		manifest.ContextStatus = "partial"
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", nil, err
	}
	return "", parsed, ws.Write(artifact.FileInputManifest, encoded)
}

func changedOnly(s *source.Snapshot, parsed *diff.ParsedDiff) *source.Snapshot {
	kept := &source.Snapshot{Commit: s.Commit, Files: map[string]string{}, Excluded: s.Excluded}
	for _, file := range parsed.Files {
		if content, ok := s.Files[file.OldPath]; ok {
			kept.Files[file.OldPath] = content
		}
	}
	kept.Excluded += len(s.Files) - len(kept.Files)
	return kept
}
