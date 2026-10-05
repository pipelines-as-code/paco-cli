package post

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/security"
)

func postVerified(ctx context.Context, ws *artifact.Workspace, gh *ghclient.Client, repo ghclient.Repo, pr int, rev *review.Review) error {
	data, err := ws.Read(artifact.FileReview)
	if err != nil {
		return err
	}
	status, err := review.ReadStatus(ws, data)
	if err != nil {
		return err
	}
	if status.Repo != repo.String() || status.PRNumber != pr {
		return errors.New("verified review belongs to a different pull request")
	}
	mode, err := ws.Read(artifact.FileMode)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading review mode: %w", err)
	}
	if strings.TrimSpace(string(mode)) == "summary" {
		return errors.New("verified findings cannot be published from a summary-only run")
	}
	headData, err := ws.Read(artifact.FileHeadSHA)
	if err != nil || strings.TrimSpace(string(headData)) != status.HeadSHA {
		return errors.New("verified review does not match workspace head")
	}
	refs, err := gh.PullRequestMetadata(ctx, repo, pr)
	if err != nil {
		return fmt.Errorf("checking reviewed commit: %s", security.Scrub(err.Error(), gh.Token()))
	}
	if refs.HeadSHA != status.HeadSHA {
		return errors.New("pull request head changed; rerun diff and review before posting")
	}
	if refs.BaseRef != status.BaseRef || refs.TargetBaseSHA != status.TargetBaseSHA {
		return errors.New("pull request base changed; rerun diff and review before posting")
	}

	if status.State == "failed" || ws.Exists(artifact.FileFailed) {
		if len(rev.Comments) != 0 || len(rev.SummaryFindings) != 0 {
			return errors.New("failed verification contains publishable findings")
		}
		return postSticky(ctx, gh, repo, pr, marker+"\n## Paco Review\n\nVerification failed. No findings published.\n\n"+rev.Summary)
	}
	validData, err := ws.Read(artifact.FileValidLines)
	if err != nil {
		return fmt.Errorf("reading verified anchors: %w", err)
	}
	var valid map[string]map[string]bool
	if err := json.Unmarshal(validData, &valid); err != nil || valid == nil {
		return errors.New("invalid verified anchor map")
	}
	if status.Accepted != len(rev.Comments)+len(rev.SummaryFindings) {
		return errors.New("verified finding count does not match output")
	}
	if status.Posted != 0 && status.Posted != len(rev.Comments) {
		return errors.New("inconsistent verified publication count")
	}
	for _, c := range rev.Comments {
		if c.Line < 1 || c.Body == "" || !valid[c.Path][strconv.Itoa(c.Line)] {
			return errors.New("verified finding has an invalid inline anchor; rerun review")
		}
	}
	// Prior issue deduplication was done with full feedback during verification.
	// Do not drop a distinct finding just because a reviewer used the same line.
	inline := buildInlineComments(rev.Comments, valid, nil)
	if len(inline) != len(rev.Comments) {
		return errors.New("verified inline findings failed publication validation")
	}
	body := marker + "\n## Paco Review\n\n" + rev.Summary
	if status.State == "partial" {
		body += "\n\nReview coverage is incomplete."
	}
	for _, limitation := range status.Limitations {
		body += "\n- " + limitation
	}
	if status.Accepted == 0 {
		body += "\n\nNo new verified findings. This is not a guarantee that the change is defect-free."
	}
	for _, finding := range rev.SummaryFindings {
		body += fmt.Sprintf("\n\n[%s] `%s` (before line %d): %s",
			strings.ToUpper(finding.Severity), finding.Path, finding.Line, finding.Body)
	}
	body += fmt.Sprintf("\n\n<sub>Reviewed commit: %s. %d verified findings; %d summary-only.</sub>",
		status.HeadSHA, status.Accepted, len(rev.SummaryFindings))
	if reason := security.ScanSecrets(body, gh.Token()); reason != "" {
		return errors.New("verified summary contains credential-shaped content; publication withheld")
	}
	if err := postSticky(ctx, gh, repo, pr, body); err != nil {
		return err
	}
	if len(inline) > 0 && status.Posted == 0 {
		if err := gh.CreateReview(ctx, repo, pr, status.HeadSHA,
			"Paco verified findings; see the summary for coverage limitations.", inline); err != nil {
			fmt.Printf("Verified inline posting failed: %s\n", security.Scrub(err.Error(), gh.Token()))
			if noteErr := postSticky(ctx, gh, repo, pr, body+"\n\nInline publication failed; the verified findings were not confirmed posted."); noteErr != nil {
				return fmt.Errorf("inline publication and summary update failed: %s", security.Scrub(noteErr.Error(), gh.Token()))
			}
			return errors.New("verified inline publication failed; check the run logs before retrying")
		}
		status.Posted = len(inline)
	}
	if err := review.WriteStatus(ws, status); err != nil {
		return err
	}
	applyLabels(ctx, gh, repo, pr, rev.ReviewScore.Rating, rev.SecuritySensitive)
	return nil
}
