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
- Test external commands via fake scripts in PATH, not mocks

## Dependencies

- `make vendor` after adding or updating dependencies
- Commit `vendor/` and `go.sum`
- Keep dependencies minimal

## Code Review

- Security-sensitive code (redaction, scanning, trust filtering,
  prompt construction, subprocess execution) requires owner review
- All `gh` and `opencode` calls go through `command.Runner`
- Never use `sh -c` or shell interpolation for subprocess execution
