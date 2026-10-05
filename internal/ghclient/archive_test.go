package ghclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"
)

func TestSourceArchiveDoesNotForwardToken(t *testing.T) {
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.Header.Get("Authorization"), "")
		assert.Equal(t, r.URL.Query().Get("signature"), "signed")
		_, _ = io.WriteString(w, "archive")
	}))
	defer download.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Path, "/repos/o/r/tarball/abc123")
		assert.Assert(t, r.Header.Get("Authorization") != "")
		w.Header().Set("Location", download.URL+"/archive?signature=signed")
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()
	data, err := testClient(t, api, "/").SourceArchive(context.Background(), Repo{"o", "r"}, "abc123")
	assert.NilError(t, err)
	assert.Equal(t, string(data), "archive")
}

func TestSourceArchiveRejectsInvalidLink(t *testing.T) {
	for _, location := range []string{"", "/relative", "https://user:password@example.com/archive"} {
		t.Run(location, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			_, err := testClient(t, api, "/").SourceArchive(context.Background(), Repo{"o", "r"}, "sha")
			assert.ErrorContains(t, err, "invalid archive URL")
		})
	}
}
