// Package ghtest provides an httptest-backed fake GitHub API for tests.
package ghtest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
)

// Token is the bearer token the fake client sends.
const Token = "fake-gh-token"

// Call is one request received by the fake.
type Call struct {
	Key  string // "METHOD /path"
	Body string
}

// Fake routes requests by "METHOD /path"; unregistered routes return 404.
type Fake struct {
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	calls  []Call
}

// New starts a fake server and returns it with a client pointed at it.
func New(t *testing.T) (*Fake, *ghclient.Client) {
	t.Helper()
	f := &Fake{routes: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	c, err := ghclient.New(ghclient.Config{
		Token:         Token,
		APIBaseURL:    srv.URL + "/",
		GraphQLURL:    srv.URL + "/graphql",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.calls = append(f.calls, Call{Key: key, Body: string(body)})
	h := f.routes[key]
	f.mu.Unlock()
	if h == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		return
	}
	h(w, r)
}

// Handle registers a handler for "METHOD /path".
func (f *Fake) Handle(key string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[key] = h
}

// JSON registers a 200 response with a fixed JSON body.
func (f *Fake) JSON(key, body string) { f.Status(key, http.StatusOK, body) }

// Status registers a fixed status code and JSON body.
func (f *Fake) Status(key string, code int, body string) {
	f.Handle(key, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	})
}

// Calls returns received requests matching key, or all of them when key is empty.
func (f *Fake) Calls(key string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if key == "" || c.Key == key {
			out = append(out, c)
		}
	}
	return out
}

// Called reports whether a request matching key was received.
func (f *Fake) Called(key string) bool { return len(f.Calls(key)) > 0 }
