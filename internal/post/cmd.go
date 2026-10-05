package post

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/spf13/cobra"
)

const marker = "<!-- paco-review -->"

type scoreLabel struct {
	Rating int
	Word   string
	Name   string
	Color  string
}

// scoreLabels is indexed by rating-1.
var scoreLabels = []scoreLabel{
	{1, "Trivial", "paco/review-trivial", "0e8a16"},
	{2, "Easy", "paco/review-easy", "5be3a0"},
	{3, "Moderate", "paco/review-moderate", "fbca04"},
	{4, "Hard", "paco/review-hard", "d93f0b"},
	{5, "Very Hard", "paco/review-very-hard", "b60205"},
}

// labelFor treats an out-of-range rating as moderate.
func labelFor(rating int) scoreLabel {
	if rating < 1 || rating > len(scoreLabels) {
		rating = 3
	}
	return scoreLabels[rating-1]
}

type Options struct {
	Repo      string
	PRNumber  int
	Workspace string
	// GitHub is the API client; nil builds one from the environment.
	GitHub *ghclient.Client
}

const withheldBody = marker + "\n## Paco Review \U0001F6AB\n\nPaco review withheld: the model output tripped a security filter (possible prompt injection). Maintainers can check the PipelineRun logs for details."

func Command() *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:   "post",
		Short: "Post review results to the pull request",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Run(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.Repo, "repo", "", "GitHub repository (owner/name)")
	cmd.Flags().IntVar(&opts.PRNumber, "pr", 0, "Pull request number")
	cmd.Flags().StringVar(&opts.Workspace, "workspace", ".", "Workspace directory for artifacts")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")

	return cmd
}

func Run(ctx context.Context, opts Options) error {
	ws := &artifact.Workspace{Dir: opts.Workspace}
	pr := opts.PRNumber

	rev, validLines, existingInline, err := loadArtifacts(ws)
	if err != nil {
		return err
	}

	repo, err := ghclient.ParseRepo(opts.Repo)
	if err != nil {
		return err
	}
	gh := opts.GitHub
	if gh == nil {
		if gh, err = ghclient.FromEnv(); err != nil {
			return fmt.Errorf("configuring GitHub access: %w", err)
		}
	}

	if ws.Exists(artifact.FileSecurityBlock) {
		return postSticky(ctx, gh, repo, pr, withheldBody)
	}

	// Belt-and-braces rescan with this step's own GitHub token
	reviewData, _ := ws.Read(artifact.FileReview)
	if reason := security.ScanSecrets(string(reviewData), gh.Token()); reason != "" {
		fmt.Printf("Security filter tripped: %s; withholding review.\n", reason)
		return postSticky(ctx, gh, repo, pr, withheldBody)
	}

	summary := rev.Summary
	if summary == "" {
		summary = "Paco review completed."
	}

	inlineComments := buildInlineComments(rev.Comments, validLines, existingInline)

	headSHAData, _ := ws.Read(artifact.FileHeadSHA)
	headSHA := strings.TrimSpace(string(headSHAData))
	if headSHA == "" {
		headSHA = "unknown"
	}

	modeData, _ := ws.Read(artifact.FileMode)
	mode := strings.TrimSpace(string(modeData))
	if mode == "" {
		mode = "review"
	}

	failed := ws.Exists(artifact.FileFailed)

	statusEmoji, findingsLine, scoreLine := buildStatusLines(failed, mode, len(inlineComments), rev)

	stickyBody := fmt.Sprintf("%s\n## Paco Review %s\n\n%s\n%s\n%s\n<sub>Reviewed commit: %s</sub>",
		marker, statusEmoji, summary, scoreLine, findingsLine, headSHA)

	if err := postSticky(ctx, gh, repo, pr, stickyBody); err != nil {
		return err
	}

	if !failed {
		applyLabels(ctx, gh, repo, pr, rev.ReviewScore.Rating, rev.SecuritySensitive)
	}

	if len(inlineComments) > 0 {
		err := gh.CreateReview(ctx, repo, pr, headSHA,
			"Paco inline comments -- see the Paco Review summary comment for the overview.", inlineComments)
		if err != nil {
			fmt.Printf("Inline review failed (scrubbed):\n%s\n", security.Scrub(err.Error(), gh.Token()))
			noteBody := stickyBody + "\n\n> [!NOTE]\n> Some inline comments could not be posted (a line number may fall outside the diff)."
			_ = postSticky(ctx, gh, repo, pr, noteBody)
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

func buildInlineComments(comments []review.Comment, validLines, existingInline map[string]map[string]bool) []ghclient.ReviewComment {
	var result []ghclient.ReviewComment
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
		result = append(result, ghclient.ReviewComment{
			Path: c.Path,
			Line: c.Line,
			Side: "RIGHT",
			Body: fmt.Sprintf("**[%s]** %s", strings.ToUpper(sev), c.Body),
		})
	}
	return result
}

func buildStatusLines(failed bool, mode string, commentCount int, rev *review.Review) (string, string, string) {
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
		findingsLine = "\nNo new review findings.\n"
	}

	label := labelFor(rev.ReviewScore.Rating)
	scoreLine = fmt.Sprintf("\n**Review difficulty:** %d/5 (%s)", label.Rating, label.Word)
	if rev.ReviewScore.Reason != "" {
		scoreLine += ": " + rev.ReviewScore.Reason
	}
	scoreLine += "\n"
	return statusEmoji, findingsLine, scoreLine
}

func postSticky(ctx context.Context, gh *ghclient.Client, repo ghclient.Repo, pr int, body string) error {
	comments, err := gh.IssueComments(ctx, repo, pr)
	if err != nil {
		return fmt.Errorf("listing pull request comments: %w", err)
	}
	for _, c := range comments {
		if strings.Contains(c.Body, marker) {
			if err := gh.UpdateComment(ctx, repo, c.ID, body); err != nil {
				return fmt.Errorf("updating the Paco summary comment: %w", err)
			}
			fmt.Println("Updated Paco summary comment")
			return nil
		}
	}
	if err := gh.CreateComment(ctx, repo, pr, body); err != nil {
		return fmt.Errorf("creating the Paco summary comment: %w", err)
	}
	fmt.Println("Created Paco summary comment")
	return nil
}

// applyLabels is best effort: label failures never fail the step.
func applyLabels(ctx context.Context, gh *ghclient.Client, repo ghclient.Repo, pr, rating int, securitySensitive bool) {
	target := labelFor(rating)
	warn := func(action string, err error) {
		if err != nil {
			fmt.Printf("Warning: could not %s: %v\n", action, err)
		}
	}

	warn("create label "+target.Name, gh.EnsureLabel(ctx, repo, target.Name, target.Color, "Paco review difficulty"))

	for _, sl := range scoreLabels {
		if sl.Name == target.Name {
			continue
		}
		warn("remove label "+sl.Name, gh.RemoveLabel(ctx, repo, pr, sl.Name))
	}

	warn("add label "+target.Name, gh.AddLabel(ctx, repo, pr, target.Name))

	if securitySensitive {
		warn("create label security-review", gh.EnsureLabel(ctx, repo, "security-review", "b60205", "Flagged as security-sensitive by Paco"))
		warn("add label security-review", gh.AddLabel(ctx, repo, pr, "security-review"))
	}
}
