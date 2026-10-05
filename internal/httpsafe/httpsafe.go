// Package httpsafe holds HTTP client hardening shared by the GitHub and
// model clients.
package httpsafe

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Origin returns scheme://host:port with the default port made explicit, so
// https://h and https://h:443 compare equal.
func Origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// CheckRedirect refuses redirects that leave the origin of the first request
// or, unless allowInsecure is set, downgrade from https.
func CheckRedirect(allowInsecure bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after %d redirects", len(via))
		}
		if !allowInsecure && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing non-https redirect")
		}
		if Origin(req.URL) != Origin(via[0].URL) {
			return fmt.Errorf("refusing cross-origin redirect")
		}
		return nil
	}
}
