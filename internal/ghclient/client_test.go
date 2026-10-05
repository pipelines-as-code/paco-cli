package ghclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
)

func TestDeriveURLs(t *testing.T) {
	tests := []struct {
		name        string
		apiURL      string
		host        string
		graphqlURL  string
		wantREST    string
		wantGraphQL string
		wantErr     string
	}{
		{name: "public default", wantREST: "https://api.github.com/", wantGraphQL: "https://api.github.com/graphql"},
		{name: "explicit github.com host", host: "github.com", wantREST: "https://api.github.com/", wantGraphQL: "https://api.github.com/graphql"},
		{name: "public api url without slash", apiURL: "https://api.github.com", wantREST: "https://api.github.com/", wantGraphQL: "https://api.github.com/graphql"},
		{name: "enterprise host", host: "ghe.example", wantREST: "https://ghe.example/api/v3/", wantGraphQL: "https://ghe.example/api/graphql"},
		{name: "enterprise api url", apiURL: "https://ghe.example/api/v3", wantREST: "https://ghe.example/api/v3/", wantGraphQL: "https://ghe.example/api/graphql"},
		{name: "proxy prefixed api url kept as given", apiURL: "https://proxy.example/custom/", wantREST: "https://proxy.example/custom/", wantGraphQL: "https://proxy.example/api/graphql"},
		{name: "api url wins over host", apiURL: "https://ghe.one/api/v3/", host: "ghe.two", wantREST: "https://ghe.one/api/v3/", wantGraphQL: "https://ghe.one/api/graphql"},
		{name: "graphql override", host: "ghe.example", graphqlURL: "https://gql.example/graphql", wantREST: "https://ghe.example/api/v3/", wantGraphQL: "https://gql.example/graphql"},
		{name: "ghe.com tenant host", host: "acme.ghe.com", wantREST: "https://api.acme.ghe.com/", wantGraphQL: "https://api.acme.ghe.com/graphql"},
		{name: "ghe.com tenant api url", apiURL: "https://api.acme.ghe.com", wantREST: "https://api.acme.ghe.com/", wantGraphQL: "https://api.acme.ghe.com/graphql"},
		{name: "http api url rejected", apiURL: "http://ghe.example/api/v3/", wantErr: "must use https"},
		{name: "userinfo rejected", apiURL: "https://u:p@ghe.example/api/v3/", wantErr: "must not contain credentials"},
		{name: "host with path rejected", host: "ghe.example/evil", wantErr: "invalid host"},
		{name: "http graphql override rejected", graphqlURL: "http://gql.example/", wantErr: "must use https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, gql, err := deriveURLs(tt.apiURL, tt.host, tt.graphqlURL)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, rest, tt.wantREST)
			assert.Equal(t, gql, tt.wantGraphQL)
		})
	}
}

func TestDeriveURLsErrorsHidePassword(t *testing.T) {
	const pw = "s3cretpw"
	tests := []struct {
		name       string
		apiURL     string
		host       string
		graphqlURL string
	}{
		{name: "api url userinfo", apiURL: "https://u:" + pw + "@ghe.example/api/v3/"},
		{name: "api url userinfo over http", apiURL: "http://u:" + pw + "@ghe.example/"},
		{name: "api url unparsable", apiURL: "https://u:" + pw + "@ghe example/%zz"},
		{name: "graphql url userinfo", graphqlURL: "https://u:" + pw + "@gql.example/graphql"},
		{name: "host userinfo", host: "u:" + pw + "@ghe.example"},
		{name: "api query", apiURL: "https://api.github.com/?access_token=" + pw},
		{name: "api fragment", apiURL: "https://api.github.com/#" + pw},
		{name: "insecure api query", apiURL: "http://api.github.com/?access_token=" + pw},
		{name: "insecure api path", apiURL: "http://api.github.com/" + pw},
		{name: "graphql query", graphqlURL: "https://api.github.com/graphql?access_token=" + pw},
		{name: "graphql fragment", graphqlURL: "https://api.github.com/graphql#" + pw},
		{name: "insecure graphql fragment", graphqlURL: "http://api.github.com/graphql#" + pw},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := deriveURLs(tt.apiURL, tt.host, tt.graphqlURL)
			assert.Assert(t, err != nil)
			assert.Assert(t, !strings.Contains(err.Error(), pw), err.Error())
		})
	}
}

func TestConfigFromEnvToken(t *testing.T) {
	tests := []struct {
		name    string
		gh      string
		github  string
		want    string
		wantErr bool
	}{
		{name: "GH_TOKEN preferred", gh: "a", github: "b", want: "a"},
		{name: "GITHUB_TOKEN fallback", github: "b", want: "b"},
		{name: "missing token", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", tt.gh)
			t.Setenv("GITHUB_TOKEN", tt.github)
			t.Setenv("GITHUB_API_URL", "")
			t.Setenv("GH_HOST", "")
			t.Setenv("GITHUB_GRAPHQL_URL", "")
			cfg, err := ConfigFromEnv()
			if tt.wantErr {
				assert.ErrorContains(t, err, "GH_TOKEN")
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, cfg.Token, tt.want)
		})
	}
}

func TestNewRejectsInsecureWithoutOptIn(t *testing.T) {
	_, err := New(Config{Token: "t", APIBaseURL: "http://x/", GraphQLURL: "https://x/graphql"})
	assert.ErrorContains(t, err, "must use https")
}

func testClient(t *testing.T, srv *httptest.Server, prefix string) *Client {
	t.Helper()
	c, err := New(Config{Token: "secret-token", APIBaseURL: srv.URL + prefix, GraphQLURL: srv.URL + "/graphql", AllowInsecure: true})
	assert.NilError(t, err)
	return c
}

func TestProxyPrefixedBasePath(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"id":1}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, "/custom/")
	assert.NilError(t, c.CheckAccess(context.Background(), Repo{"o", "r"}))
	assert.Equal(t, gotPath, "/custom/repos/o/r")
	assert.Equal(t, gotAuth, "Bearer secret-token")
}

func TestRedirectsAreRestricted(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect target must not be reached (auth=%q)", r.Header.Get("Authorization"))
	}))
	defer other.Close()

	tests := []struct {
		name   string
		target func(srvURL string) string
	}{
		{name: "unrelated host", target: func(string) string { return other.URL + "/repos/o/r" }},
		{name: "changed port", target: func(srvURL string) string {
			return strings.Replace(other.URL, "127.0.0.1", "localhost", 1) + "/repos/o/r"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tt.target(""), http.StatusFound)
			}))
			defer srv.Close()
			c := testClient(t, srv, "/")
			err := c.CheckAccess(context.Background(), Repo{"o", "r"})
			assert.ErrorContains(t, err, "cross-origin redirect")
		})
	}
}

func TestHTTPSDowngradeRedirectRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/repos/o/r", http.StatusFound)
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", APIBaseURL: srv.URL + "/", GraphQLURL: srv.URL + "/graphql"})
	assert.NilError(t, err)
	c.http.Transport.(*authTransport).base = srv.Client().Transport
	err = c.CheckAccess(context.Background(), Repo{"o", "r"})
	assert.ErrorContains(t, err, "non-https redirect")
}

func TestTokenOnlySentToConfiguredOrigin(t *testing.T) {
	var otherAuth string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherAuth = r.Header.Get("Authorization")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	c := testClient(t, srv, "/")
	req, err := http.NewRequest(http.MethodGet, other.URL, nil)
	assert.NilError(t, err)
	resp, err := c.http.Do(req)
	assert.NilError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, otherAuth, "")
}

func TestEnsureLabel(t *testing.T) {
	tests := []struct {
		name       string
		createCode int
		createBody string
		wantPatch  bool
		wantErr    bool
	}{
		{name: "created", createCode: http.StatusCreated, createBody: `{"name":"l"}`},
		{
			name: "already exists is reconciled", createCode: http.StatusUnprocessableEntity,
			createBody: `{"message":"Validation Failed","errors":[{"resource":"Label","code":"already_exists","field":"name"}]}`,
			wantPatch:  true,
		},
		{
			name: "other validation error is returned", createCode: http.StatusUnprocessableEntity,
			createBody: `{"message":"Validation Failed","errors":[{"resource":"Label","code":"invalid","field":"color"}]}`,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var patched map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/labels":
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					assert.Equal(t, body["color"], "b60205")
					assert.Equal(t, body["description"], "desc")
					w.WriteHeader(tt.createCode)
					_, _ = io.WriteString(w, tt.createBody)
				case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/labels/security-review":
					_ = json.NewDecoder(r.Body).Decode(&patched)
					_, _ = io.WriteString(w, `{}`)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()

			err := testClient(t, srv, "/").EnsureLabel(context.Background(), Repo{"o", "r"}, "security-review", "b60205", "desc")
			if tt.wantErr {
				assert.Assert(t, err != nil)
				return
			}
			assert.NilError(t, err)
			if tt.wantPatch {
				assert.Equal(t, patched["color"], "b60205")
				assert.Equal(t, patched["description"], "desc")
			} else {
				assert.Assert(t, patched == nil)
			}
		})
	}
}

func TestRemoveLabelIgnoresNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Label does not exist"}`)
	}))
	defer srv.Close()
	assert.NilError(t, testClient(t, srv, "/").RemoveLabel(context.Background(), Repo{"o", "r"}, 1, "x"))
}

func TestIssueCommentsPagination(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Path, "/repos/o/r/issues/7/comments")
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `[{"id":2,"user":{"login":"b"},"body":"two"}]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/7/comments?page=2>; rel="next"`, srv.URL))
		_, _ = io.WriteString(w, `[{"id":1,"user":{"login":"a"},"body":"one"}]`)
	}))
	defer srv.Close()

	got, err := testClient(t, srv, "/").IssueComments(context.Background(), Repo{"o", "r"}, 7)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, []IssueComment{{ID: 1, Login: "a", Body: "one"}, {ID: 2, Login: "b", Body: "two"}})
}

func TestReviewThreads(t *testing.T) {
	pages := []string{
		`{"data":{"repository":{"pullRequest":{"reviewThreads":{
			"pageInfo":{"hasNextPage":true,"endCursor":"c1"},
			"nodes":[{"isResolved":false,"comments":{"nodes":[
				{"path":"a.go","line":3,"originalLine":1,"body":"x","author":{"login":"alice"},"pullRequestReview":{"state":"COMMENTED"}},
				{"path":"b.go","line":null,"originalLine":9,"body":"y","author":null,"pullRequestReview":null}
			]}}]}}}}}`,
		`{"data":{"repository":{"pullRequest":{"reviewThreads":{
			"pageInfo":{"hasNextPage":false,"endCursor":"c2"},
			"nodes":[{"isResolved":true,"comments":{"nodes":[
				{"path":"c.go","line":5,"body":"z","author":{"login":"bob"},"pullRequestReview":{"state":"DISMISSED"}}
			]}}]}}}}}`,
	}
	var mu sync.Mutex
	var cursors []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Path, "/graphql")
		assert.Equal(t, r.Header.Get("Authorization"), "Bearer secret-token")
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		assert.NilError(t, json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		cursors = append(cursors, req.Variables["endCursor"])
		n := len(cursors)
		mu.Unlock()
		assert.Equal(t, req.Variables["number"], float64(4))
		_, _ = io.WriteString(w, pages[n-1])
	}))
	defer srv.Close()

	got, err := testClient(t, srv, "/").ReviewThreads(context.Background(), Repo{"o", "r"}, 4)
	assert.NilError(t, err)
	assert.DeepEqual(t, cursors, []any{nil, "c1"})
	assert.DeepEqual(t, got, []ThreadComment{
		{Login: "alice", Path: "a.go", Line: 3, Body: "x", ReviewState: "COMMENTED"},
		{Login: "unknown", Path: "b.go", Line: 9, Body: "y", ReviewState: "COMMENTED"},
		{Login: "bob", Path: "c.go", Line: 5, Body: "z", Resolved: true, ReviewState: "DISMISSED"},
	})
}

func TestReviewThreadsGraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"boom"}]}`)
	}))
	defer srv.Close()
	_, err := testClient(t, srv, "/").ReviewThreads(context.Background(), Repo{"o", "r"}, 1)
	assert.ErrorContains(t, err, "boom")
}

func TestPaginationBeyondFiftyPages(t *testing.T) {
	tests := []struct {
		name string
		list func(*Client) (int, string, error)
	}{
		{name: "issue comments", list: func(c *Client) (int, string, error) {
			items, err := c.IssueComments(context.Background(), Repo{"o", "r"}, 1)
			if len(items) == 0 {
				return 0, "", err
			}
			return len(items), items[len(items)-1].Body, err
		}},
		{name: "reviews", list: func(c *Client) (int, string, error) {
			items, err := c.Reviews(context.Background(), Repo{"o", "r"}, 1)
			if len(items) == 0 {
				return 0, "", err
			}
			return len(items), items[len(items)-1].Body, err
		}},
		{name: "threads", list: func(c *Client) (int, string, error) {
			items, err := c.ReviewThreads(context.Background(), Repo{"o", "r"}, 1)
			if len(items) == 0 {
				return 0, "", err
			}
			return len(items), items[len(items)-1].Body, err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := 1
				if r.URL.Path == "/graphql" {
					var request struct {
						Variables struct {
							Cursor *string `json:"endCursor"`
						} `json:"variables"`
					}
					assert.NilError(t, json.NewDecoder(r.Body).Decode(&request))
					if request.Variables.Cursor != nil {
						n, err := strconv.Atoi(*request.Variables.Cursor)
						assert.NilError(t, err)
						page = n + 1
					}
					assert.Assert(t, page <= 51)
					_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{
						"pageInfo":{"hasNextPage":%t,"endCursor":"%d"},
						"nodes":[{"comments":{"nodes":[{"body":"page %d"}]}}]
					}}}}}`, page < 51, page, page)
					return
				}
				if raw := r.URL.Query().Get("page"); raw != "" {
					n, err := strconv.Atoi(raw)
					assert.NilError(t, err)
					page = n
				}
				assert.Assert(t, page <= 51)
				if page < 51 {
					w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=%d>; rel="next"`, r.Host, r.URL.Path, page+1))
				}
				_, _ = fmt.Fprintf(w, `[{"body":"page %d"}]`, page)
			}))
			defer server.Close()
			count, last, err := tt.list(testClient(t, server, "/"))
			assert.NilError(t, err)
			assert.Equal(t, count, 51)
			assert.Equal(t, last, "page 51")
		})
	}
}

func TestRESTRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name string
		list func(*Client) error
	}{
		{name: "reviews", list: func(c *Client) error {
			_, err := c.Reviews(context.Background(), Repo{"o", "r"}, 1)
			return err
		}},
		{name: "comments", list: func(c *Client) error {
			_, err := c.IssueComments(context.Background(), Repo{"o", "r"}, 1)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, r.URL.Path))
				_, _ = io.WriteString(w, `[]`)
			}))
			defer server.Close()
			err := tt.list(testClient(t, server, "/"))
			assert.ErrorContains(t, err, "pagination did not advance")
		})
	}
}

func TestReviewThreadsRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
	}{
		{name: "missing cursor"},
		{name: "repeated cursor", cursor: "same"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{
					"pageInfo":{"hasNextPage":true,"endCursor":%q},"nodes":[]
				}}}}}`, tt.cursor)
			}))
			defer server.Close()
			_, err := testClient(t, server, "/").ReviewThreads(context.Background(), Repo{"o", "r"}, 1)
			assert.ErrorContains(t, err, "pagination did not advance")
		})
	}
}

func TestCreateReviewPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.Method, http.MethodPost)
		assert.Equal(t, r.URL.Path, "/repos/o/r/pulls/3/reviews")
		assert.NilError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	err := testClient(t, srv, "/").CreateReview(context.Background(), Repo{"o", "r"}, 3, "sha", "body",
		[]ReviewComment{{Path: "a.go", Line: 2, Side: "RIGHT", Body: "b"}})
	assert.NilError(t, err)
	assert.DeepEqual(t, got, map[string]any{
		"commit_id": "sha", "body": "body", "event": "COMMENT",
		"comments": []any{map[string]any{"path": "a.go", "line": float64(2), "side": "RIGHT", "body": "b"}},
	})
}

func TestParseRepo(t *testing.T) {
	tests := []struct {
		in      string
		want    Repo
		wantErr bool
	}{
		{in: "o/r", want: Repo{"o", "r"}},
		{in: "o", wantErr: true},
		{in: "/r", wantErr: true},
		{in: "o/r/x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseRepo(tt.in)
			if tt.wantErr {
				assert.Assert(t, err != nil)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}

func TestParseCommentID(t *testing.T) {
	tests := []struct {
		in     string
		want   int64
		wantOK bool
	}{
		{in: "", wantOK: false},
		{in: "123", want: 123, wantOK: true},
		{in: "abc", wantOK: false},
		{in: "-1", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ParseCommentID(tt.in)
			assert.Equal(t, ok, tt.wantOK)
			assert.Equal(t, got, tt.want)
		})
	}
}
