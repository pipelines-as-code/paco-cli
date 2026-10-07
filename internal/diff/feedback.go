package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/security"
)

// feedbackSource is the subset of the GitHub client the feedback digest needs.
type feedbackSource interface {
	ReviewThreads(ctx context.Context, r ghclient.Repo, pr int) ([]ghclient.ThreadComment, error)
	Reviews(ctx context.Context, r ghclient.Repo, pr int) ([]ghclient.Review, error)
	IssueComments(ctx context.Context, r ghclient.Repo, pr int) ([]ghclient.IssueComment, error)
	Permission(ctx context.Context, r ghclient.Repo, login string) (string, error)
}

func fetchExistingFeedback(ctx context.Context, gh feedbackSource, repo ghclient.Repo, pr int, secrets ...string) ([]byte, string, artifact.TrustedFeedback, error) {
	// Each source is best effort: a failure only drops that source.
	comments, err := gh.ReviewThreads(ctx, repo, pr)
	threadsAvailable := err == nil
	if err != nil {
		fmt.Printf("Warning: could not fetch review threads: %s\n", security.Scrub(err.Error(), secrets...))
		comments = nil
	}
	reviews, err := gh.Reviews(ctx, repo, pr)
	reviewsAvailable := err == nil
	if err != nil {
		fmt.Printf("Warning: could not fetch reviews: %s\n", security.Scrub(err.Error(), secrets...))
		reviews = nil
	}
	issueComments, err := gh.IssueComments(ctx, repo, pr)
	issueCommentsAvailable := err == nil
	if err != nil {
		fmt.Printf("Warning: could not fetch issue comments: %s\n", security.Scrub(err.Error(), secrets...))
		issueComments = nil
	}

	logins := collectLogins(comments, reviews, issueComments)
	permMap, permissionsComplete := resolvePermissions(ctx, gh, repo, logins, secrets...)
	structured, err := buildTrustedFeedback(comments, permMap, secrets...)
	if err != nil {
		return nil, "", structured, err
	}
	if !threadsAvailable {
		structured.Status = "unavailable"
	} else if !reviewsAvailable || !issueCommentsAvailable || !permissionsComplete {
		structured.Status = "partial"
	}

	existingJSON, err := json.Marshal(buildExistingInlineMap(comments, permMap))
	if err != nil {
		return []byte("{}"), "", structured, err
	}
	return existingJSON, buildFeedbackDigest(comments, reviews, issueComments, permMap), structured, nil
}

func collectLogins(comments []ghclient.ThreadComment, reviews []ghclient.Review, issueComments []ghclient.IssueComment) []string {
	seen := map[string]bool{}
	var logins []string
	add := func(login string) {
		if login != "" && !seen[login] {
			seen[login] = true
			logins = append(logins, login)
		}
	}
	for _, c := range comments {
		add(c.Login)
	}
	for _, r := range reviews {
		add(r.Login)
	}
	for _, c := range issueComments {
		add(c.Login)
	}
	return logins
}

func resolvePermissions(ctx context.Context, gh feedbackSource, repo ghclient.Repo, logins []string, secrets ...string) (map[string]string, bool) {
	permMap := map[string]string{}
	complete := true
	for _, login := range logins {
		perm, err := gh.Permission(ctx, repo, login)
		if err != nil {
			fmt.Printf("Warning: could not resolve feedback author permission: %s\n", security.Scrub(err.Error(), secrets...))
			perm = "none"
			complete = false
		}
		permMap[login] = perm
	}
	return permMap, complete
}

func buildTrustedFeedback(comments []ghclient.ThreadComment, permMap map[string]string, secrets ...string) (artifact.TrustedFeedback, error) {
	result := artifact.TrustedFeedback{Version: 1, Status: "available", Comments: []artifact.TrustedComment{}}
	for i, c := range comments {
		if c.Body == "" || c.Path == "" || c.Line <= 0 || strings.Contains(c.Body, "<!-- paco-review -->") ||
			c.Resolved || c.ReviewState == "DISMISSED" || !isTrusted(permMap[c.Login]) {
			continue
		}
		if len(result.Comments) >= 100 || len(c.Path) > 1024 || security.ScanSecrets(c.Path, secrets...) != "" {
			result.Truncated = true
			continue
		}
		body := security.Scrub(c.Body, secrets...)
		truncated := len(body) > 2000
		if truncated {
			end := 2000
			for !utf8.ValidString(body[:end]) {
				end--
			}
			body = body[:end]
			result.Truncated = true
		}
		result.Comments = append(result.Comments, artifact.TrustedComment{
			ID: fmt.Sprintf("inline-%d", i+1), Path: c.Path, Line: c.Line, Body: body, Truncated: truncated,
		})
		encoded, err := json.Marshal(result)
		if err != nil {
			return result, err
		}
		if len(encoded) > maxFeedbackBytes {
			result.Comments = result.Comments[:len(result.Comments)-1]
			result.Truncated = true
		}
	}
	if result.Truncated {
		result.Status = "partial"
	}
	return result, nil
}

func isTrusted(perm string) bool {
	return perm == "write" || perm == "admin" || perm == "maintain"
}

func buildExistingInlineMap(comments []ghclient.ThreadComment, permMap map[string]string) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	for _, c := range comments {
		if c.Path == "" || c.Line == 0 || c.Resolved || c.ReviewState == "DISMISSED" || !isTrusted(permMap[c.Login]) {
			continue
		}
		if result[c.Path] == nil {
			result[c.Path] = map[string]bool{}
		}
		result[c.Path][strconv.Itoa(c.Line)] = true
	}
	return result
}

func digestBody(body string) string {
	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.ReplaceAll(body, "\r", " ")
	if len(body) > 400 {
		body = body[:400]
	}
	return body
}

func buildFeedbackDigest(comments []ghclient.ThreadComment, reviews []ghclient.Review, issueComments []ghclient.IssueComment, permMap map[string]string) string {
	var lines []string

	for _, c := range comments {
		if c.Body == "" || c.Path == "" || strings.Contains(c.Body, "<!-- paco-review -->") ||
			c.Resolved || c.ReviewState == "DISMISSED" || !isTrusted(permMap[c.Login]) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s on %s:%d: %s", c.Login, c.Path, c.Line, digestBody(c.Body)))
	}

	for _, r := range reviews {
		if r.Body == "" || strings.Contains(r.Body, "<!-- paco-review -->") {
			continue
		}
		if strings.HasPrefix(r.Body, "## Paco Review") || strings.HasPrefix(r.Body, "Paco inline comments") {
			continue
		}
		if r.State == "DISMISSED" || !isTrusted(permMap[r.Login]) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- review by %s: %s", r.Login, digestBody(r.Body)))
	}

	for _, c := range issueComments {
		if c.Body == "" || strings.Contains(c.Body, "<!-- paco-review -->") || !isTrusted(permMap[c.Login]) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- comment by %s: %s", c.Login, digestBody(c.Body)))
	}

	return strings.Join(lines, "\n")
}
