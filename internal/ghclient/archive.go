package ghclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/google/go-github/v92/github"
	"github.com/pipelines-as-code/paco-cli/internal/source"
)

// SourceArchive requests an archive at an immutable commit. GitHub may return
// a signed codeload URL; authTransport never forwards our token to that origin.
func (c *Client) SourceArchive(ctx context.Context, repo Repo, commit string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	link, _, err := c.rest.Repositories.GetArchiveLink(ctx, repo.Owner, repo.Name, github.Tarball,
		&github.RepositoryContentGetOptions{Ref: commit}, 0)
	if err != nil {
		return nil, archiveError(err)
	}
	if link == nil || link.Host == "" || link.User != nil || link.Fragment != "" {
		return nil, errors.New("GitHub returned an invalid archive URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.String(), nil)
	if err != nil {
		return nil, errors.New("could not construct source archive request")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, archiveError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source archive returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, source.MaxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading source archive: %w", err)
	}
	if len(data) > source.MaxArchiveBytes {
		return nil, errors.New("source archive exceeds 32 MiB")
	}
	return data, nil
}

func archiveError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("fetching source archive: %w", err)
}
