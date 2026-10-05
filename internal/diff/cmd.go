package diff

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"github.com/pipelines-as-code/paco-cli/internal/toolchain"
	"github.com/spf13/cobra"
)

func Command() *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Fetch PR diff and existing feedback",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Run(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.Repo, "repo", "", "GitHub repository (owner/name)")
	cmd.Flags().IntVar(&opts.PRNumber, "pr", 0, "Pull request number")
	cmd.Flags().StringVar(&opts.CommentID, "comment-id", "", "Trigger comment ID (optional)")
	cmd.Flags().StringVar(&opts.Workspace, "workspace", ".", "Workspace directory for artifacts")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")

	return cmd
}

const (
	maxDiffBytes     = 200000
	maxFeedbackBytes = 30000
)

type Options struct {
	Repo      string
	PRNumber  int
	CommentID string
	Workspace string
	// GitHub is the API client; nil builds one from the environment.
	GitHub *ghclient.Client
}

func Run(ctx context.Context, opts Options) error {
	ws := &artifact.Workspace{Dir: opts.Workspace}
	for _, name := range []string{
		artifact.FileSource, artifact.FileSourceBefore, artifact.FileInputManifest, artifact.FileExistingFeedbackJSON,
		artifact.FileDiff, artifact.FileValidLines, artifact.FileExistingInline, artifact.FileExistingFeedback,
		artifact.FileHeadSHA, artifact.FileReviewRules, artifact.FileToolchains, artifact.FileError,
		artifact.FileReview, artifact.FileMode, artifact.FileFailed, artifact.FileSecurityBlock, artifact.FileStatus,
	} {
		if err := os.Remove(ws.Path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clearing %s: %w", name, err)
		}
	}
	pr := opts.PRNumber

	repo, err := ghclient.ParseRepo(opts.Repo)
	if err != nil {
		return ws.WriteSkip(fmt.Sprintf("Paco: %v.", err))
	}
	gh := opts.GitHub
	if gh == nil {
		if gh, err = ghclient.FromEnv(); err != nil {
			return ws.WriteSkip(fmt.Sprintf("Paco: could not configure GitHub access: %v.", err))
		}
	}

	if err := gh.CheckAccess(ctx, repo); err != nil {
		return ws.WriteSkip(fmt.Sprintf("Paco: the Pipelines-as-Code GitHub App token could not access %s.", repo))
	}

	addEyesReaction(ctx, gh, repo, pr, opts.CommentID)

	refs, err := gh.PullRequestMetadata(ctx, repo, pr)
	if err != nil {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not look up pull request #%d on %s.", pr, repo))
	}
	if refs.HeadSHA == "" || refs.BaseRef == "" {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not read pull request #%d refs on %s.", pr, repo))
	}
	if err := ws.Write(artifact.FileHeadSHA, []byte(refs.HeadSHA)); err != nil {
		return err
	}

	rawDiff, err := gh.PullRequestDiff(ctx, repo, pr)
	if err != nil {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not fetch the diff for pull request #%d.", pr))
	}

	if len(rawDiff) > maxDiffBytes {
		return ws.WriteSkip(fmt.Sprintf(
			"Paco: PR diff is too large (%d bytes, limit is %d), so review was skipped instead of using a truncated diff.",
			len(rawDiff), maxDiffBytes,
		))
	}

	redactedDiff := security.Scrub(rawDiff, gh.Token())
	if err := ws.Write(artifact.FileDiff, []byte(redactedDiff)); err != nil {
		return err
	}

	parsed, err := Parse(redactedDiff)
	if err != nil {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not parse the pull request diff: %s.", security.Scrub(err.Error(), gh.Token())))
	}
	validLinesJSON, err := json.Marshal(ValidLines(parsed))
	if err != nil {
		return err
	}
	if err := ws.Write(artifact.FileValidLines, validLinesJSON); err != nil {
		return err
	}

	existingInline, feedbackDigest, structuredFeedback, err := fetchExistingFeedback(ctx, gh, repo, pr, gh.Token())
	if err != nil {
		fmt.Printf("Warning: could not fetch existing feedback: %s\n", security.Scrub(err.Error(), gh.Token()))
		existingInline = []byte("{}")
		feedbackDigest = ""
		structuredFeedback = artifact.TrustedFeedback{Version: 1, Status: "unavailable", Comments: []artifact.TrustedComment{}}
	}
	if err := ws.Write(artifact.FileExistingInline, existingInline); err != nil {
		return err
	}

	if len(feedbackDigest) > maxFeedbackBytes {
		feedbackDigest = feedbackDigest[:maxFeedbackBytes]
	}
	feedbackDigest = security.Scrub(feedbackDigest, gh.Token())
	if err := ws.Write(artifact.FileExistingFeedback, []byte(feedbackDigest)); err != nil {
		return err
	}

	manifest := artifact.InputManifest{
		Version: 1, Repo: repo.String(), PRNumber: pr, HeadSHA: refs.HeadSHA,
		TargetBaseSHA: refs.TargetBaseSHA, DiffDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(redactedDiff))),
		ContextStatus: "complete",
		Head:          artifact.ContextState{Status: "unavailable"},
		Before:        artifact.ContextState{Status: "unavailable"},
	}
	baseRef := refs.TargetBaseSHA
	if baseRef == "" {
		baseRef = refs.BaseRef
		manifest.Limitations = append(manifest.Limitations, "Target-base SHA is unavailable; trusted rules and toolchains used the base branch.")
		manifest.Before.Reason = "Target-base SHA is unavailable."
	} else {
		manifest.MergeBaseSHA, err = gh.MergeBase(ctx, repo, refs.TargetBaseSHA, refs.HeadSHA)
		if err != nil {
			manifest.Before.Reason = security.Scrub(err.Error(), gh.Token())
		}
	}
	fetchReviewRules(ctx, gh, repo, baseRef, ws)
	fetchToolchains(ctx, gh, repo, baseRef, ws)

	head, headErr := fetchSource(ctx, gh, repo, refs.HeadSHA)
	if headErr != nil && refs.HeadRepo != "" && refs.HeadRepo != repo.String() {
		if fork, parseErr := ghclient.ParseRepo(refs.HeadRepo); parseErr == nil {
			head, headErr = fetchSource(ctx, gh, fork, refs.HeadSHA)
		}
	}
	if headErr != nil {
		manifest.Head.Reason = security.Scrub(headErr.Error(), gh.Token())
	}
	var before *source.Snapshot
	if manifest.MergeBaseSHA != "" {
		before, err = fetchSource(ctx, gh, repo, manifest.MergeBaseSHA)
		if err == nil {
			err = source.ValidateCombined(head, before)
		}
		if err != nil {
			before = nil
			manifest.Before.Reason = security.Scrub(err.Error(), gh.Token())
		}
	}
	after, err := gh.PullRequestMetadata(ctx, repo, pr)
	if err != nil {
		return ws.WriteSkip("Paco: could not recheck pull request refs after context collection.")
	}
	if after != refs {
		return ws.WriteSkip("Paco: pull request head or base changed during context collection; retry the review.")
	}
	encodedFeedback, err := json.Marshal(structuredFeedback)
	if err != nil {
		return err
	}
	if err := ws.Write(artifact.FileExistingFeedbackJSON, encodedFeedback); err != nil {
		return err
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
			fmt.Printf("Warning: %s context unavailable: %s\n", item.name, item.state.Reason)
			continue
		}
		item.state.Status, item.state.Excluded = "available", item.snapshot.Excluded
		if item.snapshot.Excluded > 0 {
			item.state.Status = "partial"
			manifest.ContextStatus = "partial"
		}
		encoded, err := json.Marshal(item.snapshot)
		if err != nil {
			return err
		}
		if err := ws.Write(item.name, encoded); err != nil {
			return err
		}
	}
	for _, file := range parsed.Files {
		if file.Binary {
			manifest.Limitations = append(manifest.Limitations, "Binary file changes have no text hunk context.")
			break
		}
	}
	for _, file := range parsed.Files {
		for _, hunk := range file.Hunks {
			if !hunk.Complete {
				manifest.Limitations = append(manifest.Limitations, "A diff hunk is incomplete.")
				break
			}
		}
	}
	if len(manifest.Limitations) > 0 {
		manifest.ContextStatus = "partial"
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := ws.Write(artifact.FileInputManifest, encoded); err != nil {
		return err
	}

	fmt.Printf("Existing feedback digest: %d bytes\n", len(feedbackDigest))
	fmt.Printf("Diff size: %d bytes\n", len(rawDiff))

	return nil
}

func fetchSource(ctx context.Context, gh *ghclient.Client, repo ghclient.Repo, headSHA string) (*source.Snapshot, error) {
	data, err := gh.SourceArchive(ctx, repo, headSHA)
	if err != nil {
		return nil, err
	}
	snapshot, err := source.FromArchive(data, headSHA, gh.Token())
	if err != nil {
		return nil, err
	}
	if err := source.ValidateCombined(snapshot, nil); err != nil {
		return nil, err
	}
	fmt.Printf("Source snapshot: %d files, %d excluded\n", len(snapshot.Files), snapshot.Excluded)
	return snapshot, nil
}

func addEyesReaction(ctx context.Context, gh *ghclient.Client, repo ghclient.Repo, pr int, commentID string) {
	if id, ok := ghclient.ParseCommentID(commentID); ok {
		_ = gh.AddCommentReaction(ctx, repo, id, "eyes")
		return
	}
	_ = gh.AddIssueReaction(ctx, repo, pr, "eyes")
}

func fetchReviewRules(ctx context.Context, gh *ghclient.Client, repo ghclient.Repo, baseBranch string, ws *artifact.Workspace) {
	content, err := gh.FileContent(ctx, repo, ".tekton/ai/REVIEW.md", baseBranch)
	if err != nil || len(content) == 0 {
		fmt.Printf("No review rules file found at %s:.tekton/ai/REVIEW.md, skipping\n", baseBranch)
		return
	}
	if err := ws.Write(artifact.FileReviewRules, []byte(security.Scrub(string(content), gh.Token()))); err != nil {
		fmt.Printf("Warning: could not write review rules: %v\n", err)
		return
	}
	fmt.Printf("Fetched review rules: %d bytes\n", len(content))
}

// fetchToolchains lists the base branch root once, fetches only the known
// version files present there, and writes the detected versions.
func fetchToolchains(ctx context.Context, gh *ghclient.Client, repo ghclient.Repo, baseBranch string, ws *artifact.Workspace) {
	names, err := gh.RootFileNames(ctx, repo, baseBranch)
	if err != nil {
		fmt.Printf("Could not list %s root, skipping toolchain detection\n", baseBranch)
		return
	}
	present := map[string]bool{}
	for _, name := range names {
		present[name] = true
	}

	files := map[string][]byte{}
	for _, name := range toolchain.Files() {
		if !present[name] {
			continue
		}
		content, err := gh.FileContent(ctx, repo, name, baseBranch)
		if err != nil {
			continue
		}
		files[name] = content
	}

	versions := toolchain.Detect(files)
	if len(versions) == 0 {
		return
	}
	if err := ws.Write(artifact.FileToolchains, []byte(security.Scrub(string(toolchain.Format(versions)), gh.Token()))); err != nil {
		fmt.Printf("Warning: could not write toolchain versions: %v\n", err)
		return
	}
	for _, v := range versions {
		fmt.Printf("Detected %s %s from %s\n", v.Language, v.Version, v.Source)
	}
}
