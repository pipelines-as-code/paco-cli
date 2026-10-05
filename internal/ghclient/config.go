package ghclient

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	publicAPIURL     = "https://api.github.com/"
	publicGraphQLURL = "https://api.github.com/graphql"
)

// Config describes how to reach the GitHub API.
type Config struct {
	Token      string
	APIBaseURL string
	GraphQLURL string
	// AllowInsecure permits plain-HTTP endpoints. It is only meant for
	// tests against httptest servers and is never set from the environment.
	AllowInsecure bool
}

// ConfigFromEnv reads the token from GH_TOKEN or GITHUB_TOKEN and the
// endpoints from GITHUB_API_URL, GH_HOST and GITHUB_GRAPHQL_URL.
func ConfigFromEnv() (Config, error) {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return Config{}, errors.New("no GitHub token: set GH_TOKEN or GITHUB_TOKEN")
	}
	rest, graphql, err := deriveURLs(os.Getenv("GITHUB_API_URL"), os.Getenv("GH_HOST"), os.Getenv("GITHUB_GRAPHQL_URL"))
	if err != nil {
		return Config{}, err
	}
	return Config{Token: token, APIBaseURL: rest, GraphQLURL: graphql}, nil
}

// deriveURLs resolves the REST and GraphQL endpoints. GITHUB_API_URL wins
// over GH_HOST; GITHUB_GRAPHQL_URL overrides the derived GraphQL endpoint.
func deriveURLs(apiURL, host, graphqlURL string) (string, string, error) {
	rest, graphql := publicAPIURL, publicGraphQLURL

	switch {
	case apiURL != "":
		u, err := validateURL(apiURL, false)
		if err != nil {
			return "", "", fmt.Errorf("GITHUB_API_URL: %w", err)
		}
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		rest = u.String()
		apiHost := strings.ToLower(u.Hostname())
		graphqlPath := "/api/graphql"
		if apiHost == "api.github.com" || (strings.HasPrefix(apiHost, "api.") && isTenancyHost(apiHost)) {
			graphqlPath = "/graphql"
		}
		graphql = (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: graphqlPath}).String()
	case host != "" && !strings.EqualFold(host, "github.com"):
		u, err := validateURL("https://"+host, false)
		if err != nil || u.Path != "" || u.Host != host {
			return "", "", errors.New("GH_HOST: invalid host name")
		}
		if isTenancyHost(host) {
			rest = "https://api." + host + "/"
			graphql = "https://api." + host + "/graphql"
		} else {
			rest = "https://" + host + "/api/v3/"
			graphql = "https://" + host + "/api/graphql"
		}
	}

	if graphqlURL != "" {
		u, err := validateURL(graphqlURL, false)
		if err != nil {
			return "", "", fmt.Errorf("GITHUB_GRAPHQL_URL: %w", err)
		}
		graphql = u.String()
	}
	return rest, graphql, nil
}

// isTenancyHost reports whether host is a GHE.com (data residency)
// tenant, whose API lives on the api. subdomain.
func isTenancyHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), ".ghe.com")
}

// validateURL never echoes raw input in errors: a rejected URL may carry
// credentials, and these errors can end up in a public PR comment.
func validateURL(raw string, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("not a valid URL")
	}
	if u.User != nil {
		return nil, errors.New("URL must not contain credentials")
	}
	if u.Scheme != "https" && (!allowInsecure || u.Scheme != "http") {
		return nil, errors.New("URL must use https")
	}
	if u.Host == "" {
		return nil, errors.New("URL has no host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("URL must not contain a query or fragment")
	}
	return u, nil
}
