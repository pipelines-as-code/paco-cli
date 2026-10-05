# Agent Guidelines

## Build

- `make build` produces `bin/paco`
- `make all` builds, tests, and lints

## Formatting

- `make fumpt` formats Go files with gofumpt
- `make lint-fmt` checks formatting

## Testing

- `make test` runs tests with `-race -failfast`
- Use `gotest.tools/v3/assert` (never testify)
- Table-driven tests with `tests := []struct{...}{...}`
- PascalCase test function names, no underscores
- Test GitHub and model calls against `httptest` servers
  (`internal/ghclient/ghtest` for GitHub), not mocks

## Dependencies

- `make vendor` after adding or updating dependencies
- Commit `vendor/` and `go.sum`
- Keep dependencies minimal

## Code Review

- Security-sensitive code (redaction, scanning, trust filtering,
  prompt construction, external API clients) requires owner review
- All GitHub calls go through `internal/ghclient`
- All model calls go through `internal/model`
- No subprocesses: do not use `os/exec`
