package post

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/command"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/spf13/cobra"
)

const marker = "<!-- paco-review -->"

var scoreLabels = []struct {
	Rating int
	Name   string
	Color  string
}{
	{1, "paco/review-trivial", "0e8a16"},
	{2, "paco/review-easy", "5be3a0"},
	{3, "paco/review-moderate", "fbca04"},
	{4, "paco/review-hard", "d93f0b"},
	{5, "paco/review-very-hard", "b60205"},
}

var ratingWords = map[int]string{
	1: "Trivial", 2: "Easy", 3: "Moderate", 4: "Hard", 5: "Very Hard",
}

type Options struct {
	Repo      string
	PRNumber  string
	Workspace string
	Runner    command.Runner
}

func Command() *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:   "post",
		Short: "Post review results to the pull request",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Runner = &command.ExecRunner{}
			return Run(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.Repo, "repo", "", "GitHub repository (owner/name)")
	cmd.Flags().StringVar(&opts.PRNumber, "pr", "", "Pull request number")
	cmd.Flags().StringVar(&opts.Workspace, "workspace", ".", "Workspace directory for artifacts")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")

	return cmd
}

func Run(ctx context.Context, opts Options) error {
	ws := &artifact.Workspace{Dir: opts.Workspace}
	runner := opts.Runner
	repo := opts.Repo
	pr := opts.PRNumber

	// Load and validate artifacts
	rev, validLines, existingInline, err := loadArtifacts(ws)
	if err != nil {
		return err
	}

	// Withhold if security block exists
	if ws.Exists(artifact.FileSecurityBlock) {
		return postSticky(ctx, runner, repo, pr, marker+"\n## Paco Review \U0001F6AB\n\nPaco review withheld: the model output tripped a security filter (possible prompt injection). Maintainers can check the PipelineRun logs for details.")
	}

	// Belt-and-braces rescan with this step's own GH_TOKEN
	reviewData, _ := ws.Read(artifact.FileReview)
	reviewText := string(reviewData)
	ghToken := os.Getenv("GH_TOKEN")
	if reason := security.ScanSecrets(reviewText, ghToken); reason != "" {
		fmt.Printf("Security filter tripped: %s; withholding review.\n", reason)
		return postSticky(ctx, runner, repo, pr, marker+"\n## Paco Review \U0001F6AB\n\nPaco review withheld: the model output tripped a security filter (possible prompt injection). Maintainers can check the PipelineRun logs for details.")
	}

	summary := rev.Summary
	if summary == "" {
		summary = "Paco review completed."
	}

	// Build inline review payload
	inlineComments := buildInlineComments(rev.Comments, validLines, existingInline)

	headSHAData, _ := ws.Read(artifact.FileHeadSHA)
	headSHA := strings.TrimSpace(string(headSHAData))
	if headSHA == "" {
		headSHA = "unknown"
	}

	// Determine status
	modeData, _ := ws.Read(artifact.FileMode)
	mode := strings.TrimSpace(string(modeData))
	if mode == "" {
		mode = "review"
	}

	failed := ws.Exists(artifact.FileFailed)

	statusEmoji, findingsLine, scoreLine := buildStatusLines(failed, mode, len(inlineComments), rev, ws)

	stickyBody := fmt.Sprintf("%s\n## Paco Review %s\n\n%s\n%s\n%s\n<sub>Reviewed commit: %s</sub>",
		marker, statusEmoji, summary, scoreLine, findingsLine, headSHA)

	if err := postSticky(ctx, runner, repo, pr, stickyBody); err != nil {
		return err
	}

	// Apply labels
	if !failed {
		applyLabels(ctx, runner, repo, pr, rev.ReviewScore.Rating, rev.SecuritySensitive)
	}

	// Post inline review
	if len(inlineComments) > 0 {
		payload := map[string]interface{}{
			"commit_id": headSHA,
			"body":      "Paco inline comments -- see the Paco Review summary comment for the overview.",
			"event":     "COMMENT",
			"comments":  inlineComments,
		}
		payloadJSON, _ := json.Marshal(payload)

		result, err := runner.Run(ctx, "gh", []string{
			"api", "--method", "POST",
			fmt.Sprintf("repos/%s/pulls/%s/reviews", repo, pr),
			"--input", "-",
		}, nil, payloadJSON)

		if err != nil || result.ExitCode != 0 {
			stderr := security.Redact(string(result.Stderr))
			fmt.Printf("Inline review failed; gh api output (scrubbed):\n%s\n", stderr)
			noteBody := stickyBody + "\n\n> [!NOTE]\n> Some inline comments could not be posted (a line number may fall outside the diff)."
			_ = postSticky(ctx, runner, repo, pr, noteBody)
		} else {
			fmt.Printf("Posted %d new inline comment(s)\n", len(inlineComments))
		}
	} else {
		fmt.Println("No new inline comments to post")
	}

	return nil
}

func loadArtifacts(ws *artifact.Workspace) (*review.Review, map[string]map[string]bool, map[string]map[string]bool, error) {
	reviewData, err := ws.Read(artifact.FileReview)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cannot read review: %w", err)
	}
	var rev review.Review
	if err := json.Unmarshal(reviewData, &rev); err != nil {
		rev = review.Review{Summary: "Could not parse Paco model output.", Comments: []review.Comment{}}
		_ = ws.Write(artifact.FileFailed, nil)
	}

	validData, _ := ws.Read(artifact.FileValidLines)
	var validLines map[string]map[string]bool
	if err := json.Unmarshal(validData, &validLines); err != nil {
		validLines = map[string]map[string]bool{}
	}

	existingData, _ := ws.Read(artifact.FileExistingInline)
	var existingInline map[string]map[string]bool
	if err := json.Unmarshal(existingData, &existingInline); err != nil {
		existingInline = map[string]map[string]bool{}
	}

	return &rev, validLines, existingInline, nil
}

func buildInlineComments(comments []review.Comment, validLines, existingInline map[string]map[string]bool) []map[string]interface{} {
	var result []map[string]interface{}
	for _, c := range comments {
		if c.Path == "" || c.Line == 0 || c.Body == "" {
			continue
		}
		lineStr := strconv.Itoa(c.Line)
		if validLines[c.Path] == nil || !validLines[c.Path][lineStr] {
			continue
		}
		if existingInline[c.Path] != nil && existingInline[c.Path][lineStr] {
			continue
		}
		sev := c.Severity
		if sev == "" {
			sev = "medium"
		}
		result = append(result, map[string]interface{}{
			"path": c.Path,
			"line": c.Line,
			"side": "RIGHT",
			"body": fmt.Sprintf("**[%s]** %s", strings.ToUpper(sev), c.Body),
		})
	}
	return result
}

func buildStatusLines(failed bool, mode string, commentCount int, rev *review.Review, ws *artifact.Workspace) (string, string, string) {
	if failed {
		return "⚠️", "", ""
	}
	if mode == "summary" {
		return "\U0001F4DD", "", ""
	}

	var statusEmoji, findingsLine, scoreLine string

	if commentCount > 0 {
		statusEmoji = "\U0001F50D"
		findingsLine = fmt.Sprintf("\n%d new inline comment(s) found.\n", commentCount)
	} else {
		statusEmoji = "✅"
		findingsLine = "\nNo new review comments found at this time. Nice work!\n"
	}

	rating := rev.ReviewScore.Rating
	if rating < 1 || rating > 5 {
		rating = 3
	}
	word := ratingWords[rating]
	scoreLine = fmt.Sprintf("\n**Review difficulty:** %d/5 (%s)", rating, word)
	if rev.ReviewScore.Reason != "" {
		scoreLine += " — " + rev.ReviewScore.Reason
	}
	scoreLine += "\n"

	_ = ws
	return statusEmoji, findingsLine, scoreLine
}

func postSticky(ctx context.Context, runner command.Runner, repo, pr, body string) error {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo format: %s", repo)
	}

	result, err := runner.Run(ctx, "gh", []string{
		"api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%s/comments", repo, pr),
		"--jq", `[.[] | select(.body | contains("<!-- paco-review -->"))][0].id // empty`,
	}, nil, nil)

	var commentID string
	if err == nil && result.ExitCode == 0 {
		commentID = strings.TrimSpace(string(result.Stdout))
	}

	if commentID != "" {
		_, err = runner.Run(ctx, "gh", []string{
			"api", "-X", "PATCH",
			fmt.Sprintf("repos/%s/issues/comments/%s", repo, commentID),
			"-f", "body=" + body,
		}, nil, nil)
		if err != nil {
			return err
		}
		fmt.Println("Updated Paco summary comment")
	} else {
		_, err = runner.Run(ctx, "gh", []string{
			"api",
			fmt.Sprintf("repos/%s/issues/%s/comments", repo, pr),
			"-f", "body=" + body,
		}, nil, nil)
		if err != nil {
			return err
		}
		fmt.Println("Created Paco summary comment")
	}
	return nil
}

func applyLabels(ctx context.Context, runner command.Runner, repo, pr string, rating int, securitySensitive bool) {
	if rating < 1 || rating > 5 {
		rating = 3
	}

	var targetLabel, targetColor string
	for _, sl := range scoreLabels {
		if sl.Rating == rating {
			targetLabel = sl.Name
			targetColor = sl.Color
			break
		}
	}

	// Ensure label exists
	_, _ = runner.Run(ctx, "gh", []string{
		"label", "create", targetLabel,
		"--color", targetColor,
		"--description", "Paco review difficulty",
		"--force", "-R", repo,
	}, nil, nil)

	// Remove other score labels
	for _, sl := range scoreLabels {
		if sl.Name == targetLabel {
			continue
		}
		_, _ = runner.Run(ctx, "gh", []string{
			"pr", "edit", pr, "-R", repo,
			"--remove-label", sl.Name,
		}, nil, nil)
	}

	// Add target label
	_, _ = runner.Run(ctx, "gh", []string{
		"pr", "edit", pr, "-R", repo,
		"--add-label", targetLabel,
	}, nil, nil)

	// Security review label
	_, _ = runner.Run(ctx, "gh", []string{
		"label", "create", "security-review",
		"--color", "b60205",
		"--description", "Flagged as security-sensitive by Paco",
		"--force", "-R", repo,
	}, nil, nil)

	if securitySensitive {
		_, _ = runner.Run(ctx, "gh", []string{
			"pr", "edit", pr, "-R", repo,
			"--add-label", "security-review",
		}, nil, nil)
	}
}
