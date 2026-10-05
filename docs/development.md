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
- Repository tools only read validated head/comparison snapshots and the
  collected diff in `internal/source`. They cannot read arbitrary refs or
  workspace files.
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
internal/eval/        opt-in quality evaluation and human-adjudicated scoring
```

## Review evaluations

The original synthetic corpus is in `internal/review/testdata/eval/cases.json`.
It contains 30 cases, including safe changes, cross-file defects, and held-out
cases. The snippets are reduced examples, not complete buildable repositories.
Offline tests check fixture integrity and pipeline behavior; they do not
measure model accuracy. Expected issue labels need human review before using
them to make a default-rollout decision.

The runner renders each changed file as a whole-file replacement hunk. This
keeps fixture preparation independent of Git, but makes more lines eligible
as inline anchors than a minimal diff would. Use identical inputs for paired
runs, and check real PR diffs separately before drawing rollout conclusions.

`public.json` records two public PR candidates and immutable revisions, with
links to the source benchmark. Their labels have not been independently
adjudicated, and their source is not bundled or fetched automatically.
Prepare licensed, redacted fixtures at those exact revisions before including
them in a live comparison. Do not feed later fixes or review comments into
the reviewer's context.

List synthetic cases without credentials or network access:

```shell
go run ./cmd/paco-eval --list
```

Live runs require explicit case selection and token allowances. They use the
configured model backend, disable web search, and never post to GitHub.
As with `paco review`, API-enforced structured output is off by default;
local validation still applies. Use `--no-structured-output=false` only if
the provider permits that feature. Reports record this setting.

```shell
go run ./cmd/paco-eval --live \
  --cases division-guard-removed,division-guard-retained \
  --strategy verified --repeat 3 \
  --max-input-tokens 100000 --max-output-tokens 12000 \
  --output /tmp/paco-verified.json
```

Run `--strategy single` separately with the same cases/model/effort for the
baseline. The numbers above are example allowances, not a cost estimate.
Input accounting is provider-reported and can overshoot by one request.
Output allowances cap model requests. Completed results are saved between
cases. No live runs belong in `make test` or CI.

Reports record source and prompt digests, build revision, strategy, model, token usage,
latency, failures, expected labels, and findings. Expected labels are not
written to the model workspace. Compare repeated held-out runs rather than
tuning prompts against the evaluation set.

For scoring, a human supplies finding and summary judgments, for example:

```json
{
  "findings": [
    {"run_id":"division-guard-removed/verified/1","finding":0,"issue":"zero-divisor","anchor_valid":true}
  ],
  "summaries": [
    {"run_id":"division-guard-removed/verified/1","accurate":true}
  ]
}
```

Finding indexes are zero-based: inline findings first, then summary-only
findings. Each finding must match a labeled issue or receive the explicit
`false_positive` judgment. New valid issues require updating the labels;
missing judgments cause an error rather than being counted as false positives.

```shell
go run ./cmd/paco-eval --score /tmp/paco-verified.json \
  --judgments /tmp/paco-judgments.json
```

The scorer reports issue-level precision/recall, duplicate findings, failed
runs, and negative-case false findings. Repeated findings for one issue count
as duplicate noise in the precision denominator. Failed runs still count
toward missed issues. Zero predictions produce precision 0, not a perfect score.
The scorer also reports human-assessed summary accuracy and anchor validity.
Each run needs a summary judgment, including clean or failed runs.
Do not mix strategies into one report when comparing them.

Proposed rollout targets are 90% adjudicated precision, false findings on at
most 5% of negative-case runs, and recall within five percentage points of
single-pass review. Report counts and variability alongside percentages.
These are targets, not measured results or approval to change the default.

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
