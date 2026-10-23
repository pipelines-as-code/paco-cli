package post

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/progress"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/pipelines-as-code/paco-cli/internal/status"
	"github.com/spf13/cobra"
)

const marker = status.Marker

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
	CommentID string
	// RunID must match the run that ran diff; empty uses diff's.
	RunID             string
	StatusLabels      bool
	StatusLabelPrefix string
	LogWriter         io.Writer
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
	cmd.Flags().StringVar(&opts.CommentID, "comment-id", "", "Trigger comment ID (optional)")
	cmd.Flags().StringVar(&opts.RunID, "run-id", "", "Run identifier passed to diff; defaults to the one diff recorded")
	cmd.Flags().BoolVar(&opts.StatusLabels, "status-labels", true, "Set a status label on the pull request")
	cmd.Flags().StringVar(&opts.StatusLabelPrefix, "status-label-prefix", status.DefaultPrefix, "Prefix of the status labels")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")

	return cmd
}

func Run(ctx context.Context, opts Options) (err error) {
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

	log := progress.New(opts.LogWriter, []string{gh.Token()})
	t, proceed, err := newTarget(ctx, ws, gh, repo, pr, opts, log)
	if err != nil {
		return err
	}
	if !proceed {
		log.Line("Status: a newer Paco run owns the review comment; not publishing this run")
		return nil
	}
	defer func() {
		if err != nil {
			t.fail(ctx)
		}
	}()

	if ws.Exists(artifact.FileSecurityBlock) {
		return t.withhold(ctx)
	}

	// Belt-and-braces rescan with this step's own GitHub token
	reviewData, _ := ws.Read(artifact.FileReview)
	if reason := security.ScanSecrets(string(reviewData), gh.Token()); reason != "" {
		log.Line("Security filter tripped: %s; withholding review", reason)
		return t.withhold(ctx)
	}
	if rev.Verified {
		return postVerified(ctx, ws, t, rev, log)
	}
	if ws.Exists(review.FileStatus) {
		return errors.New("verification status exists but review output is not verified; rerun review")
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
	if failed || mode == "summary" {
		inlineComments = nil
	}

	statusEmoji, findingsLine, scoreLine := buildStatusLines(failed, mode, len(inlineComments), rev)

	stickyBody := fmt.Sprintf("%s\n## Paco Review %s\n\n%s\n%s\n%s\n<sub>Reviewed commit: %s</sub>",
		marker, statusEmoji, summary, scoreLine, findingsLine, headSHA)

	state := status.Clean
	switch {
	case failed:
		state = status.Failed
	case mode == "summary":
		state = status.Summarized
	case len(inlineComments) > 0:
		state = status.Findings
	}
	if err := t.sticky(ctx, stickyBody, state); err != nil {
		return err
	}

	if !failed {
		applyLabels(ctx, gh, repo, pr, rev.ReviewScore.Rating, rev.SecuritySensitive)
	}

	if len(inlineComments) > 0 {
		err := gh.CreateReview(ctx, repo, pr, headSHA,
			"Paco inline comments -- see the Paco Review summary comment for the overview.", inlineComments)
		if err != nil {
			log.Line("Inline publication failed: %s", err.Error())
			noteBody := stickyBody + "\n\n> [!NOTE]\n> Some inline comments could not be posted (a line number may fall outside the diff)."
			state = status.Failed
			if err := t.sticky(ctx, noteBody, state); err != nil {
				log.Line("Warning: could not add the inline failure note: %s", err.Error())
				t.fail(ctx)
				return nil
			}
		} else {
			log.Line("Publication completed: %d inline findings posted", len(inlineComments))
		}
	} else {
		if failed {
			log.Line("Publication completed: failed review summary posted; no findings published")
		} else {
			log.Line("Publication completed: summary posted; no new inline findings")
		}
	}

	t.finish(ctx, state)
	return nil
}

// target is where post publishes. Without a status reporter it behaves as
// before status reporting existed.
type target struct {
	gh   *ghclient.Client
	repo ghclient.Repo
	pr   int
	rep  *status.Reporter
	st   *status.Sticky
}

// newTarget reports false when a newer run owns the review comment, in which
// case this run must not write anything.
func newTarget(ctx context.Context, ws *artifact.Workspace, gh *ghclient.Client, repo ghclient.Repo, pr int, opts Options, log *progress.Logger) (*target, bool, error) {
	t := &target{gh: gh, repo: repo, pr: pr}
	claim, ok := status.ReadClaim(ws)
	if !ok {
		return t, true, nil
	}
	if opts.RunID != "" && opts.RunID != claim.RunID {
		log.Line("Warning: --run-id does not match the run that fetched the diff; status reporting disabled")
		return t, true, nil
	}
	t.rep = status.New(ctx, status.Config{
		GitHub: gh, Repo: repo, PR: pr, CommentID: opts.CommentID, RunID: claim.RunID,
		Labels: opts.StatusLabels, Prefix: opts.StatusLabelPrefix, Log: log,
	})
	if t.rep == nil {
		return t, true, nil
	}
	st, proceed, err := t.rep.Resolve(ctx, claim)
	if err != nil {
		return nil, false, fmt.Errorf("listing pull request comments: %w", err)
	}
	t.st = st
	return t, proceed, nil
}

func (t *target) sticky(ctx context.Context, body string, state status.State) error {
	if t.rep == nil {
		return postSticky(ctx, t.gh, t.repo, t.pr, body)
	}
	st, err := t.rep.Publish(ctx, t.st, body, state)
	if err != nil {
		return err
	}
	t.st = st
	return nil
}

func (t *target) withhold(ctx context.Context) error {
	if err := t.sticky(ctx, withheldBody, status.Failed); err != nil {
		return err
	}
	t.finish(ctx, status.Failed)
	return nil
}

func (t *target) finish(ctx context.Context, state status.State) {
	if t.rep != nil {
		t.rep.Update(ctx, state)
	}
}

func (t *target) fail(ctx context.Context) {
	if t.rep != nil {
		t.rep.Fail(ctx)
	}
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
			progress.New(nil, []string{gh.Token()}).Line("Publication: summary comment updated")
			return nil
		}
	}
	if _, err := gh.CreateComment(ctx, repo, pr, body); err != nil {
		return fmt.Errorf("creating the Paco summary comment: %w", err)
	}
	progress.New(nil, []string{gh.Token()}).Line("Publication: summary comment created")
	return nil
}

// applyLabels is best effort: label failures never fail the step.
func applyLabels(ctx context.Context, gh *ghclient.Client, repo ghclient.Repo, pr, rating int, securitySensitive bool) {
	target := labelFor(rating)
	warn := func(action string, err error) {
		if err != nil {
			progress.New(nil, []string{gh.Token()}).Line("Warning: could not %s: %s", action, err.Error())
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
