package diff

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/command"
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
			opts.Runner = &command.ExecRunner{}
			return Run(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.Repo, "repo", "", "GitHub repository (owner/name)")
	cmd.Flags().StringVar(&opts.PRNumber, "pr", "", "Pull request number")
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
	PRNumber  string
	CommentID string
	Workspace string
	Runner    command.Runner
}

func Run(ctx context.Context, opts Options) error {
	ws := &artifact.Workspace{Dir: opts.Workspace}
	runner := opts.Runner
	repo := opts.Repo
	pr := opts.PRNumber

	// Check GitHub App token access
	result, err := runner.Run(ctx, "gh", []string{"api", "repos/" + repo}, nil, nil)
	if err != nil || result.ExitCode != 0 {
		return ws.WriteSkip(fmt.Sprintf("Paco: the Pipelines-as-Code GitHub App token could not access %s.", repo))
	}

	// Add eyes reaction
	addEyesReaction(ctx, runner, repo, pr, opts.CommentID)

	// Get PR refs
	result, err = runner.Run(ctx, "gh", []string{
		"pr", "view", pr, "-R", repo,
		"--json", "headRefOid,baseRefName",
	}, nil, nil)
	if err != nil || result.ExitCode != 0 {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not look up pull request #%s on %s.", pr, repo))
	}
	var refs struct {
		HeadRefOid  string `json:"headRefOid"`
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal(result.Stdout, &refs); err != nil || refs.HeadRefOid == "" || refs.BaseRefName == "" {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not read pull request #%s refs on %s.", pr, repo))
	}
	if err := ws.Write(artifact.FileHeadSHA, []byte(refs.HeadRefOid)); err != nil {
		return err
	}

	// Fetch PR diff
	result, err = runner.Run(ctx, "gh", []string{"pr", "diff", pr, "-R", repo}, nil, nil)
	if err != nil || result.ExitCode != 0 {
		return ws.WriteSkip(fmt.Sprintf("Paco: could not fetch the diff for pull request #%s.", pr))
	}
	rawDiff := result.Stdout

	if len(rawDiff) > maxDiffBytes {
		return ws.WriteSkip(fmt.Sprintf(
			"Paco: PR diff is too large (%d bytes, limit is %d), so review was skipped instead of using a truncated diff.",
			len(rawDiff), maxDiffBytes,
		))
	}

	// Redact before writing
	redactedDiff := security.Redact(string(rawDiff))
	if err := ws.Write(artifact.FileDiff, []byte(redactedDiff)); err != nil {
		return err
	}

	// Parse valid added lines
	validLines, err := ParseValidLines(strings.NewReader(string(rawDiff)))
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
	existingInline, feedbackDigest, err := fetchExistingFeedback(ctx, runner, repo, pr)
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
	fetchReviewRules(ctx, runner, repo, refs.BaseRefName, ws)

	// Detect language versions declared on the base branch
	fetchToolchains(ctx, runner, repo, refs.BaseRefName, ws)

	fmt.Printf("Existing feedback digest: %d bytes\n", len(feedbackDigest))
	fmt.Printf("Diff size: %d bytes\n", len(rawDiff))

	return nil
}

func addEyesReaction(ctx context.Context, runner command.Runner, repo, pr, commentID string) {
	if commentID != "" {
		_, _ = runner.Run(ctx, "gh", []string{
			"api", "repos/" + repo + "/issues/comments/" + commentID + "/reactions",
			"-f", "content=eyes",
		}, nil, nil)
	} else {
		_, _ = runner.Run(ctx, "gh", []string{
			"api", "repos/" + repo + "/issues/" + pr + "/reactions",
			"-f", "content=eyes",
		}, nil, nil)
	}
}

func fetchReviewRules(ctx context.Context, runner command.Runner, repo, baseBranch string, ws *artifact.Workspace) {
	result, err := runner.Run(ctx, "gh", []string{
		"api", fmt.Sprintf("repos/%s/contents/.tekton/ai/REVIEW.md?ref=%s", repo, url.QueryEscape(baseBranch)),
		"--jq", ".content",
	}, nil, nil)
	if err != nil || result.ExitCode != 0 || len(result.Stdout) == 0 {
		fmt.Printf("No review rules file found at %s:.tekton/ai/REVIEW.md, skipping\n", baseBranch)
		return
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(result.Stdout)))
	if err != nil || len(decoded) == 0 {
		return
	}
	if err := ws.Write(artifact.FileReviewRules, decoded); err != nil {
		fmt.Printf("Warning: could not write review rules: %v\n", err)
		return
	}
	fmt.Printf("Fetched review rules: %d bytes\n", len(decoded))
}

// fetchToolchains lists the base branch root once, fetches only the known
// version files present there, and writes the detected versions.
func fetchToolchains(ctx context.Context, runner command.Runner, repo, baseBranch string, ws *artifact.Workspace) {
	ref := url.QueryEscape(baseBranch)
	result, err := runner.Run(ctx, "gh", []string{
		"api", fmt.Sprintf("repos/%s/contents?ref=%s", repo, ref),
		"--jq", `.[] | select(.type == "file") | .name`,
	}, nil, nil)
	if err != nil || result.ExitCode != 0 {
		fmt.Printf("Could not list %s root, skipping toolchain detection\n", baseBranch)
		return
	}
	present := map[string]bool{}
	for name := range strings.Lines(string(result.Stdout)) {
		present[strings.TrimSpace(name)] = true
	}

	files := map[string][]byte{}
	for _, name := range toolchain.Files() {
		if !present[name] {
			continue
		}
		result, err := runner.Run(ctx, "gh", []string{
			"api", fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, name, ref),
			"--jq", ".content",
		}, nil, nil)
		if err != nil || result.ExitCode != 0 {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(result.Stdout)))
		if err != nil {
			continue
		}
		files[name] = decoded
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
