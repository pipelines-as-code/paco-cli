// Package ghclient is paco's only path to the GitHub API.
package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/pipelines-as-code/paco-cli/internal/httpsafe"
)

const (
	requestTimeout = 2 * time.Minute
	perPage        = 100
)

// Repo identifies a repository as owner/name.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// ParseRepo parses "owner/name".
func ParseRepo(s string) (Repo, error) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, fmt.Errorf("invalid repo format: %s", s)
	}
	return Repo{Owner: owner, Name: name}, nil
}

// Client wraps go-github for REST and a plain POST for the one GraphQL query.
type Client struct {
	token      string
	rest       *github.Client
	http       *http.Client
	graphqlURL string
}

// New builds a client whose token is only sent to the configured origins and
// whose redirects may not leave the request origin or downgrade from https.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("no GitHub token configured")
	}
	restURL, err := validateURL(cfg.APIBaseURL, cfg.AllowInsecure)
	if err != nil {
		return nil, err
	}
	gqlURL, err := validateURL(cfg.GraphQLURL, cfg.AllowInsecure)
	if err != nil {
		return nil, err
	}
	base := restURL.String()
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}

	httpClient := &http.Client{
		Timeout: requestTimeout,
		Transport: &authTransport{
			base:          http.DefaultTransport,
			token:         cfg.Token,
			allowInsecure: cfg.AllowInsecure,
			origins: map[string]bool{
				httpsafe.Origin(restURL): true,
				httpsafe.Origin(gqlURL):  true,
			},
		},
		CheckRedirect: httpsafe.CheckRedirect(cfg.AllowInsecure),
	}

	rest, err := github.NewClient(
		github.WithHTTPClient(httpClient),
		github.WithURLs(&base, &base),
		github.WithUserAgent("paco-cli"),
	)
	if err != nil {
		return nil, err
	}
	return &Client{token: cfg.Token, rest: rest, http: httpClient, graphqlURL: gqlURL.String()}, nil
}

// FromEnv builds a client from ConfigFromEnv.
func FromEnv() (*Client, error) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return New(cfg)
}

// Token returns the token in use, so callers can scan output for it.
func (c *Client) Token() string { return c.token }

type authTransport struct {
	base          http.RoundTripper
	token         string
	origins       map[string]bool
	allowInsecure bool
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.allowInsecure && req.URL.Scheme != "https" {
		return nil, errors.New("refusing non-https request")
	}
	if t.origins[httpsafe.Origin(req.URL)] {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// CheckAccess verifies the token can read the repository.
func (c *Client) CheckAccess(ctx context.Context, r Repo) error {
	_, _, err := c.rest.Repositories.Get(ctx, r.Owner, r.Name)
	return err
}

// PullRequestRefs returns the head commit SHA and base branch name.
func (c *Client) PullRequestRefs(ctx context.Context, r Repo, pr int) (headSHA, baseRef string, err error) {
	p, _, err := c.rest.PullRequests.Get(ctx, r.Owner, r.Name, pr)
	if err != nil {
		return "", "", err
	}
	return p.GetHead().GetSHA(), p.GetBase().GetRef(), nil
}

// PullRequestDiff returns the unified diff of the pull request.
func (c *Client) PullRequestDiff(ctx context.Context, r Repo, pr int) (string, error) {
	d, _, err := c.rest.PullRequests.GetRaw(ctx, r.Owner, r.Name, pr, github.RawOptions{Type: github.Diff})
	return d, err
}

// AddIssueReaction reacts to the pull request itself.
func (c *Client) AddIssueReaction(ctx context.Context, r Repo, pr int, content string) error {
	_, _, err := c.rest.Reactions.CreateIssueReaction(ctx, r.Owner, r.Name, pr, content)
	return err
}

// AddCommentReaction reacts to an issue comment.
func (c *Client) AddCommentReaction(ctx context.Context, r Repo, commentID int64, content string) error {
	_, _, err := c.rest.Reactions.CreateIssueCommentReaction(ctx, r.Owner, r.Name, commentID, content)
	return err
}

// FileContent returns the decoded content of a file at ref.
func (c *Client) FileContent(ctx context.Context, r Repo, path, ref string) ([]byte, error) {
	file, _, _, err := c.rest.Repositories.GetContents(ctx, r.Owner, r.Name, path, &github.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	content, err := file.GetContent()
	if err != nil {
		return nil, err
	}
	return []byte(content), nil
}

// RootFileNames lists the names of regular files at the repository root.
func (c *Client) RootFileNames(ctx context.Context, r Repo, ref string) ([]string, error) {
	_, dir, _, err := c.rest.Repositories.GetContents(ctx, r.Owner, r.Name, "", &github.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range dir {
		if e.GetType() == "file" {
			names = append(names, e.GetName())
		}
	}
	return names, nil
}

// Review is a submitted pull request review.
type Review struct {
	Login string
	Body  string
	State string
}

// Reviews lists every review on the pull request.
func (c *Client) Reviews(ctx context.Context, r Repo, pr int) ([]Review, error) {
	var out []Review
	opts := &github.ListOptions{PerPage: perPage}
	for {
		page, resp, err := c.rest.PullRequests.ListReviews(ctx, r.Owner, r.Name, pr, opts)
		if err != nil {
			return out, err
		}
		for _, rv := range page {
			out = append(out, Review{Login: rv.GetUser().GetLogin(), Body: rv.GetBody(), State: rv.GetState()})
		}
		if resp.NextPage == 0 {
			break
		}
		if resp.NextPage <= opts.Page {
			return out, errors.New("reviews pagination did not advance")
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// IssueComment is a top-level pull request comment.
type IssueComment struct {
	ID    int64
	Login string
	Body  string
}

// IssueComments lists every top-level comment on the pull request.
func (c *Client) IssueComments(ctx context.Context, r Repo, pr int) ([]IssueComment, error) {
	var out []IssueComment
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: perPage}}
	for {
		page, resp, err := c.rest.Issues.ListComments(ctx, r.Owner, r.Name, pr, opts)
		if err != nil {
			return out, err
		}
		for _, ic := range page {
			out = append(out, IssueComment{ID: ic.GetID(), Login: ic.GetUser().GetLogin(), Body: ic.GetBody()})
		}
		if resp.NextPage == 0 {
			break
		}
		if resp.NextPage <= opts.Page {
			return out, errors.New("issue comments pagination did not advance")
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// CreateComment adds a top-level comment to the pull request.
func (c *Client) CreateComment(ctx context.Context, r Repo, pr int, body string) error {
	_, _, err := c.rest.Issues.CreateComment(ctx, r.Owner, r.Name, pr, github.IssueCommentRequest{Body: body})
	return err
}

// UpdateComment replaces the body of an existing comment.
func (c *Client) UpdateComment(ctx context.Context, r Repo, id int64, body string) error {
	_, _, err := c.rest.Issues.UpdateComment(ctx, r.Owner, r.Name, id, github.IssueCommentRequest{Body: body})
	return err
}

// Permission returns the user's permission on the repository
// (admin, write, read or none).
func (c *Client) Permission(ctx context.Context, r Repo, login string) (string, error) {
	p, _, err := c.rest.Repositories.GetPermissionLevel(ctx, r.Owner, r.Name, login)
	if err != nil {
		return "", err
	}
	return p.GetPermission(), nil
}

// EnsureLabel creates the label or, if it already exists, resets its colour
// and description.
func (c *Client) EnsureLabel(ctx context.Context, r Repo, name, color, description string) error {
	_, _, err := c.rest.Issues.CreateLabel(ctx, r.Owner, r.Name, github.CreateIssueLabelRequest{
		Name: name, Color: &color, Description: &description,
	})
	if err == nil || !isAlreadyExists(err) {
		return err
	}
	_, _, err = c.rest.Issues.UpdateLabel(ctx, r.Owner, r.Name, name, github.UpdateIssueLabelRequest{
		Color: &color, Description: &description,
	})
	return err
}

func isAlreadyExists(err error) bool {
	var er *github.ErrorResponse
	if !errors.As(err, &er) || er.Response == nil || er.Response.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	for _, e := range er.Errors {
		if e.Code == "already_exists" {
			return true
		}
	}
	return false
}

// AddLabel adds a label to the pull request.
func (c *Client) AddLabel(ctx context.Context, r Repo, pr int, name string) error {
	_, _, err := c.rest.Issues.AddLabelsToIssue(ctx, r.Owner, r.Name, pr, []string{name})
	return err
}

// RemoveLabel removes a label from the pull request; a missing label is not
// an error.
func (c *Client) RemoveLabel(ctx context.Context, r Repo, pr int, name string) error {
	resp, err := c.rest.Issues.RemoveLabelForIssue(ctx, r.Owner, r.Name, pr, name)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

// ReviewComment is one inline comment of a review.
type ReviewComment struct {
	Path string
	Line int
	Side string
	Body string
}

// CreateReview submits a COMMENT review with inline comments.
func (c *Client) CreateReview(ctx context.Context, r Repo, pr int, commitID, body string, comments []ReviewComment) error {
	drafts := make([]*github.DraftReviewComment, 0, len(comments))
	for _, rc := range comments {
		drafts = append(drafts, &github.DraftReviewComment{
			Path: new(rc.Path),
			Line: new(rc.Line),
			Side: new(rc.Side),
			Body: new(rc.Body),
		})
	}
	_, _, err := c.rest.PullRequests.CreateReview(ctx, r.Owner, r.Name, pr, &github.PullRequestReviewRequest{
		CommitID: new(commitID),
		Body:     new(body),
		Event:    new("COMMENT"),
		Comments: drafts,
	})
	return err
}

// ThreadComment is one comment of an inline review thread.
type ThreadComment struct {
	Login       string
	Path        string
	Line        int
	Body        string
	Resolved    bool
	ReviewState string
}

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

type reviewThreadsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						IsResolved bool `json:"isResolved"`
						Comments   struct {
							Nodes []struct {
								Path              string  `json:"path"`
								Line              *int    `json:"line"`
								OriginalLine      *int    `json:"originalLine"`
								Body              string  `json:"body"`
								Author            *author `json:"author"`
								PullRequestReview *struct {
									State string `json:"state"`
								} `json:"pullRequestReview"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type author struct {
	Login string `json:"login"`
}

// ReviewThreads returns every comment of every inline review thread.
func (c *Client) ReviewThreads(ctx context.Context, r Repo, pr int) ([]ThreadComment, error) {
	var out []ThreadComment
	var cursor *string
	seen := make(map[string]bool)
	for {
		var resp reviewThreadsResponse
		if err := c.graphql(ctx, reviewThreadsQuery, map[string]any{
			"owner": r.Owner, "name": r.Name, "number": pr, "endCursor": cursor,
		}, &resp); err != nil {
			return out, err
		}
		threads := resp.Data.Repository.PullRequest.ReviewThreads
		for _, thread := range threads.Nodes {
			for _, cm := range thread.Comments.Nodes {
				line := 0
				if cm.Line != nil {
					line = *cm.Line
				} else if cm.OriginalLine != nil {
					line = *cm.OriginalLine
				}
				login := "unknown"
				if cm.Author != nil && cm.Author.Login != "" {
					login = cm.Author.Login
				}
				state := "COMMENTED"
				if cm.PullRequestReview != nil && cm.PullRequestReview.State != "" {
					state = cm.PullRequestReview.State
				}
				out = append(out, ThreadComment{
					Login: login, Path: cm.Path, Line: line, Body: cm.Body,
					Resolved: thread.IsResolved, ReviewState: state,
				})
			}
		}
		if !threads.PageInfo.HasNextPage {
			break
		}
		next := threads.PageInfo.EndCursor
		if next == "" || seen[next] {
			return out, errors.New("review threads pagination did not advance")
		}
		seen[next] = true
		cursor = &next
	}
	return out, nil
}

func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out *reviewThreadsResponse) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "paco-cli")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql request failed: %s", resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding graphql response: %w", err)
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", out.Errors[0].Message)
	}
	return nil
}

// ParseCommentID parses an optional numeric comment ID.
func ParseCommentID(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
