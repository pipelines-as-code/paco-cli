# Development Guide

## Prerequisites

- Go (version in `go.mod`)
- [gofumpt](https://github.com/mvdan/gofumpt)
- [golangci-lint](https://golangci-lint.run/) v2
- [pre-commit](https://pre-commit.com/)

## Setup

```shell
git clone https://github.com/pipelines-as-code/paco-cli.git
cd paco-cli
pre-commit install
```

## Make Targets

| Target | Description |
|---|---|
| `make help` | List all targets |
| `make build` | Build `bin/paco` |
| `make test` | Run tests with race detection |
| `make test-no-cache` | Run tests without cache |
| `make lint` | Run all linters |
| `make fumpt` | Format Go files |
| `make coverage` | Generate coverage profile |
| `make html-coverage` | Open coverage report |
| `make vendor` | Update vendor directory |
| `make check` | Run lint + test (CI entry point) |
| `make all` | Build, test, lint |
| `make clean` | Remove build artifacts |

## Testing

Tests use `gotest.tools/v3/assert` and follow PAC conventions:

- Table-driven tests with `tests := []struct{...}{...}`
- PascalCase test function names, no underscores
- Descriptive `name` field for `t.Run` subtests

GitHub and Claude are tested against `httptest` servers. Use
`internal/ghclient/ghtest` for a fake GitHub API that records every
request; model tests inject a fake HTTP transport. No network access
required.

## Code Layout

```
cmd/paco/main.go           # entry point
internal/cli/root.go        # cobra root, subcommand registration
internal/diff/              # paco diff: fetch, parse, feedback
internal/review/            # paco review: prompt, extract, normalize
internal/post/              # paco post: sticky, labels, inline review
internal/ghclient/            # GitHub API client (REST and GraphQL)
internal/ghclient/ghtest/     # fake GitHub API for tests
internal/model/              # Claude client (Vertex AI or Anthropic API)
internal/source/             # bounded PR snapshots and read-only tools
internal/httpsafe/           # redirect and origin checks
internal/toolchain/          # base-branch language version detection
internal/artifact/           # workspace file helpers
internal/security/           # redaction and secret scanning
```

## Releasing

Releases use [GoReleaser](https://goreleaser.com/). Tag a version
and push:

```shell
git tag v0.1.0
git push origin v0.1.0
```

Snapshot build (local validation):

```shell
goreleaser build --snapshot --clean
```
