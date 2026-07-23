package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/command"
)

const reviewThreadsQuery = `query($owner: String!, $name: String!, $number: Int!, $endCursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $endCursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          isResolved
          comments(first: 100) {
            nodes {
              path
              line
              originalLine
              body
              author { login }
              pullRequestReview { state }
            }
          }
        }
      }
    }
  }
}`

type inlineComment struct {
	Login       string `json:"login"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Body        string `json:"body"`
	Resolved    bool   `json:"resolved"`
	ReviewState string `json:"review_state"`
}

func fetchExistingFeedback(ctx context.Context, runner command.Runner, repo, pr string) ([]byte, string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return []byte("{}"), "", fmt.Errorf("invalid repo format: %s", repo)
	}
	owner, name := parts[0], parts[1]

	// Fetch review threads via GraphQL
	comments := fetchReviewThreads(ctx, runner, owner, name, pr)

	// Fetch reviews and issue comments via REST
	reviews := fetchJSONLines(ctx, runner, "repos/"+repo+"/pulls/"+pr+"/reviews")
	issueComments := fetchJSONLines(ctx, runner, "repos/"+repo+"/issues/"+pr+"/comments")

	// Collect unique commenter logins
	logins := collectLogins(comments, reviews, issueComments)

	// Resolve permissions
	permMap := resolvePermissions(ctx, runner, repo, logins)

	// Build existing inline comment map (file -> {line: true})
	existingInline := buildExistingInlineMap(comments, permMap)
	existingJSON, err := json.Marshal(existingInline)
	if err != nil {
		return []byte("{}"), "", err
	}

	// Build feedback digest
	digest := buildFeedbackDigest(comments, reviews, issueComments, permMap)

	return existingJSON, digest, nil
}

func fetchReviewThreads(ctx context.Context, runner command.Runner, owner, name, pr string) []inlineComment {
	result, err := runner.Run(ctx, "gh", []string{
		"api", "graphql", "--paginate", "--slurp",
		"-F", "owner=" + owner, "-F", "name=" + name, "-F", "number=" + pr,
		"-f", "query=" + reviewThreadsQuery,
	}, nil, nil)
	if err != nil || result.ExitCode != 0 {
		return nil
	}

	var pages []struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									Path              string                 `json:"path"`
									Line              *int                   `json:"line"`
									OriginalLine      *int                   `json:"originalLine"`
									Body              string                 `json:"body"`
									Author            struct{ Login string } `json:"author"`
									PullRequestReview struct{ State string } `json:"pullRequestReview"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}

	if err := json.Unmarshal(result.Stdout, &pages); err != nil {
		return nil
	}

	var comments []inlineComment
	for _, page := range pages {
		for _, thread := range page.Data.Repository.PullRequest.ReviewThreads.Nodes {
			for _, c := range thread.Comments.Nodes {
				line := 0
				if c.Line != nil {
					line = *c.Line
				} else if c.OriginalLine != nil {
					line = *c.OriginalLine
				}
				login := c.Author.Login
				if login == "" {
					login = "unknown"
				}
				state := c.PullRequestReview.State
				if state == "" {
					state = "COMMENTED"
				}
				comments = append(comments, inlineComment{
					Login:       login,
					Path:        c.Path,
					Line:        line,
					Body:        c.Body,
					Resolved:    thread.IsResolved,
					ReviewState: state,
				})
			}
		}
	}
	return comments
}

func fetchJSONLines(ctx context.Context, runner command.Runner, endpoint string) []map[string]interface{} {
	result, err := runner.Run(ctx, "gh", []string{
		"api", "--paginate", endpoint, "--jq", ".[]",
	}, nil, nil)
	if err != nil || result.ExitCode != 0 {
		return nil
	}

	var items []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if line == "" {
			continue
		}
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items
}

func collectLogins(comments []inlineComment, reviews, issueComments []map[string]interface{}) []string {
	seen := map[string]bool{}
	for _, c := range comments {
		if c.Login != "" {
			seen[c.Login] = true
		}
	}
	for _, r := range reviews {
		if user, ok := r["user"].(map[string]interface{}); ok {
			if login, ok := user["login"].(string); ok && login != "" {
				seen[login] = true
			}
		}
	}
	for _, c := range issueComments {
		if user, ok := c["user"].(map[string]interface{}); ok {
			if login, ok := user["login"].(string); ok && login != "" {
				seen[login] = true
			}
		}
	}
	logins := make([]string, 0, len(seen))
	for login := range seen {
		logins = append(logins, login)
	}
	return logins
}

func resolvePermissions(ctx context.Context, runner command.Runner, repo string, logins []string) map[string]string {
	permMap := map[string]string{}
	for _, login := range logins {
		result, err := runner.Run(ctx, "gh", []string{
			"api", "repos/" + repo + "/collaborators/" + login + "/permission",
			"--jq", ".permission",
		}, nil, nil)
		if err != nil || result.ExitCode != 0 {
			permMap[login] = "none"
			continue
		}
		permMap[login] = strings.TrimSpace(string(result.Stdout))
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

func buildFeedbackDigest(comments []inlineComment, reviews, issueComments []map[string]interface{}, permMap map[string]string) string {
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
		body := strings.ReplaceAll(c.Body, "\n", " ")
		body = strings.ReplaceAll(body, "\r", " ")
		if len(body) > 400 {
			body = body[:400]
		}
		lines = append(lines, fmt.Sprintf("- %s on %s:%d: %s", c.Login, c.Path, c.Line, body))
	}

	for _, r := range reviews {
		body, _ := r["body"].(string)
		if body == "" || strings.Contains(body, "<!-- paco-review -->") {
			continue
		}
		if strings.HasPrefix(body, "## Paco Review") || strings.HasPrefix(body, "Paco inline comments") {
			continue
		}
		state, _ := r["state"].(string)
		if state == "DISMISSED" {
			continue
		}
		login := "unknown"
		if user, ok := r["user"].(map[string]interface{}); ok {
			if l, ok := user["login"].(string); ok {
				login = l
			}
		}
		if !isTrusted(permMap[login]) {
			continue
		}
		body = strings.ReplaceAll(body, "\n", " ")
		body = strings.ReplaceAll(body, "\r", " ")
		if len(body) > 400 {
			body = body[:400]
		}
		lines = append(lines, fmt.Sprintf("- review by %s: %s", login, body))
	}

	for _, c := range issueComments {
		body, _ := c["body"].(string)
		if body == "" || strings.Contains(body, "<!-- paco-review -->") {
			continue
		}
		login := "unknown"
		if user, ok := c["user"].(map[string]interface{}); ok {
			if l, ok := user["login"].(string); ok {
				login = l
			}
		}
		if !isTrusted(permMap[login]) {
			continue
		}
		body = strings.ReplaceAll(body, "\n", " ")
		body = strings.ReplaceAll(body, "\r", " ")
		if len(body) > 400 {
			body = body[:400]
		}
		lines = append(lines, fmt.Sprintf("- comment by %s: %s", login, body))
	}

	return strings.Join(lines, "\n")
}
