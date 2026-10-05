package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
)

type inlineComment = ghclient.ThreadComment

// feedbackSource is the subset of the GitHub client the feedback digest needs.
type feedbackSource interface {
	ReviewThreads(ctx context.Context, r ghclient.Repo, pr int) ([]ghclient.ThreadComment, error)
	Reviews(ctx context.Context, r ghclient.Repo, pr int) ([]ghclient.Review, error)
	IssueComments(ctx context.Context, r ghclient.Repo, pr int) ([]ghclient.IssueComment, error)
	Permission(ctx context.Context, r ghclient.Repo, login string) (string, error)
}

func fetchExistingFeedback(ctx context.Context, gh feedbackSource, repo ghclient.Repo, pr int) ([]byte, string, error) {
	// Each source is best effort: a failure only drops that source.
	comments, err := gh.ReviewThreads(ctx, repo, pr)
	if err != nil {
		fmt.Printf("Warning: could not fetch review threads: %v\n", err)
		comments = nil
	}
	reviews, err := gh.Reviews(ctx, repo, pr)
	if err != nil {
		fmt.Printf("Warning: could not fetch reviews: %v\n", err)
		reviews = nil
	}
	issueComments, err := gh.IssueComments(ctx, repo, pr)
	if err != nil {
		fmt.Printf("Warning: could not fetch issue comments: %v\n", err)
		issueComments = nil
	}

	logins := collectLogins(comments, reviews, issueComments)
	permMap := resolvePermissions(ctx, gh, repo, logins)

	existingJSON, err := json.Marshal(buildExistingInlineMap(comments, permMap))
	if err != nil {
		return []byte("{}"), "", err
	}
	return existingJSON, buildFeedbackDigest(comments, reviews, issueComments, permMap), nil
}

func collectLogins(comments []inlineComment, reviews []ghclient.Review, issueComments []ghclient.IssueComment) []string {
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

func resolvePermissions(ctx context.Context, gh feedbackSource, repo ghclient.Repo, logins []string) map[string]string {
	permMap := map[string]string{}
	for _, login := range logins {
		perm, err := gh.Permission(ctx, repo, login)
		if err != nil {
			perm = "none"
		}
		permMap[login] = perm
	}
	return permMap
}

func isTrusted(perm string) bool {
	return perm == "write" || perm == "admin" || perm == "maintain"
}

func buildExistingInlineMap(comments []inlineComment, permMap map[string]string) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	for _, c := range comments {
		if c.Path == "" || c.Line == 0 {
			continue
		}
		if c.Resolved {
			continue
		}
		if c.ReviewState == "DISMISSED" {
			continue
		}
		if !isTrusted(permMap[c.Login]) {
			continue
		}
		if result[c.Path] == nil {
			result[c.Path] = map[string]bool{}
		}
		result[c.Path][fmt.Sprintf("%d", c.Line)] = true
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

func loginOrUnknown(login string) string {
	if login == "" {
		return "unknown"
	}
	return login
}

func buildFeedbackDigest(comments []inlineComment, reviews []ghclient.Review, issueComments []ghclient.IssueComment, permMap map[string]string) string {
	var lines []string

	for _, c := range comments {
		if c.Body == "" || c.Path == "" {
			continue
		}
		if strings.Contains(c.Body, "<!-- paco-review -->") {
			continue
		}
		if c.Resolved || c.ReviewState == "DISMISSED" {
			continue
		}
		if !isTrusted(permMap[c.Login]) {
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
		if r.State == "DISMISSED" {
			continue
		}
		login := loginOrUnknown(r.Login)
		if !isTrusted(permMap[login]) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- review by %s: %s", login, digestBody(r.Body)))
	}

	for _, c := range issueComments {
		if c.Body == "" || strings.Contains(c.Body, "<!-- paco-review -->") {
			continue
		}
		login := loginOrUnknown(c.Login)
		if !isTrusted(permMap[login]) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- comment by %s: %s", login, digestBody(c.Body)))
	}

	return strings.Join(lines, "\n")
}
