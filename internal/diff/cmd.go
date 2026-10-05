package diff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/security"
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

	// Check GitHub App token access
	if err := gh.CheckAccess(ctx, repo); err != nil {
		return ws.WriteSkip(fmt.Sprintf("Paco: the Pipelines-as-Code GitHub App token could not access %s.", repo))
	}

	// Add eyes reaction
	addEyesReaction(ctx, gh, repo, pr, opts.CommentID)

	// Get PR refs
	headSHA, baseRef, err := gh.PullRequestRefs(ctx, repo, pr)
	if err != nil {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not look up pull request #%d on %s.", pr, repo))
	}
	if headSHA == "" || baseRef == "" {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not read pull request #%d refs on %s.", pr, repo))
	}
	if err := ws.Write(artifact.FileHeadSHA, []byte(headSHA)); err != nil {
		return err
	}

	// Fetch PR diff
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

	// Redact before writing
	redactedDiff := security.Redact(rawDiff)
	if err := ws.Write(artifact.FileDiff, []byte(redactedDiff)); err != nil {
		return err
	}

	// Parse valid added lines
	validLines, err := ParseValidLines(strings.NewReader(rawDiff))
	if err != nil {
		return err
	}
	validLinesJSON, err := json.Marshal(validLines)
	if err != nil {
		return err
	}
	if err := ws.Write(artifact.FileValidLines, validLinesJSON); err != nil {
		return err
	}

	// Fetch existing feedback
	existingInline, feedbackDigest, err := fetchExistingFeedback(ctx, gh, repo, pr)
	if err != nil {
		fmt.Printf("Warning: could not fetch existing feedback: %v\n", err)
		existingInline = []byte("{}")
		feedbackDigest = ""
	}
	if err := ws.Write(artifact.FileExistingInline, existingInline); err != nil {
		return err
	}

	// Cap feedback at 30KB and redact
	if len(feedbackDigest) > maxFeedbackBytes {
		feedbackDigest = feedbackDigest[:maxFeedbackBytes]
	}
	feedbackDigest = security.Redact(feedbackDigest)
	if err := ws.Write(artifact.FileExistingFeedback, []byte(feedbackDigest)); err != nil {
		return err
	}

	// Discard optional artifacts from any earlier run before fetching this PR's base.
	for _, name := range []string{artifact.FileReviewRules, artifact.FileToolchains} {
		if err := os.Remove(ws.Path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clearing %s: %w", name, err)
		}
	}

	// Fetch review rules from base branch
	fetchReviewRules(ctx, gh, repo, baseRef, ws)

	// Detect language versions declared on the base branch
	fetchToolchains(ctx, gh, repo, baseRef, ws)

	fmt.Printf("Existing feedback digest: %d bytes\n", len(feedbackDigest))
	fmt.Printf("Diff size: %d bytes\n", len(rawDiff))

	return nil
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
	if err := ws.Write(artifact.FileReviewRules, content); err != nil {
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
	if err := ws.Write(artifact.FileToolchains, toolchain.Format(versions)); err != nil {
		fmt.Printf("Warning: could not write toolchain versions: %v\n", err)
		return
	}
	for _, v := range versions {
		fmt.Printf("Detected %s %s from %s\n", v.Language, v.Version, v.Source)
	}
}
