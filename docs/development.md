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
| `make build` | Build `bin/paco` |
| `make test` | Run tests with race detection |
| `make lint` | Run golangci-lint and the gofumpt check |
| `make fumpt` | Format Go files |
| `make vendor` | Tidy and vendor dependencies |
| `make check` | Lint and test (CI entry point) |

`make help` lists the rest.

## Testing

Tests use `gotest.tools/v3/assert` (not testify), are table-driven
(`tests := []struct{...}`), and have PascalCase names without
underscores.

Tests never hit the network. `internal/ghclient/ghtest` is a fake GitHub
API that records requests; model tests inject a fake HTTP transport.

## Rules

- GitHub calls go through `internal/ghclient`, model calls through
  `internal/model`.
- No subprocesses (`os/exec`).
- Repository tools only read the validated snapshot in `internal/source`.
- Run `make vendor` after changing dependencies and commit `vendor/`
  with `go.sum`.
- Changes to redaction, scanning, trust filtering, prompts, or the API
  clients need owner review.

## Code Layout

```
cmd/paco/             entry point
internal/cli/         cobra root command
internal/diff/        paco diff: fetch, parse, feedback
internal/review/      paco review: prompt, extract, normalize
internal/post/        paco post: summary comment, labels, inline review
internal/ghclient/    GitHub client (REST and GraphQL), ghtest/ fake
internal/model/       Claude client (Vertex AI or Anthropic API)
internal/source/      PR-head snapshot and read-only tools
internal/httpsafe/    redirect and origin checks
internal/toolchain/   base-branch language version detection
internal/artifact/    workspace files
internal/security/    redaction and secret scanning
```

## Releasing

Pushing a tag builds a release with [GoReleaser](https://goreleaser.com/):

```shell
git tag v0.1.0
git push origin v0.1.0
```

Local snapshot build:

```shell
goreleaser build --snapshot --clean
```
