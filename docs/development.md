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
| `make reviewbench-image` | Build the ReviewBench adapter image locally |

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
internal/reviewbench/ ReviewBench adapter
hack/paco-reviewbench/ ReviewBench adapter image and configs (not released)
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

### ReviewBench

[ReviewBench](https://github.com/review-bench/ReviewBench) scores code
reviewers on real pull requests against a multi-source golden set. Unlike
`paco-eval`, it uses whole repositories and an LLM judge. `hack/paco-reviewbench`
runs one paco review under its
[agent contract](https://github.com/review-bench/ReviewBench/blob/main/AGENT_CONTRACT.md).
It reads the mounted checkout and `diff.patch` instead of calling GitHub, then
writes `findings.json`.

The adapter differs from a production review in a few ways. It rebuilds
the before snapshot by reverse-applying the diff to the head checkout. It
does not load `.tekton/ai/REVIEW.md` or toolchain files. It anchors verified
findings on deleted lines to the nearest new-side line in the same hunk.
A failed review exits non-zero so ReviewBench retries it. A skipped or
security-blocked review writes no findings.

Settings come from the manifest's configuration labels:

| Variable | Default | Meaning |
|---|---|---|
| `RB_CONFIG_MODEL` | backend default | Model name |
| `RB_CONFIG_EFFORT` | `low` | Reasoning effort |
| `RB_CONFIG_STRATEGY` | `single` | `single` or `verified` (`--verify-findings`) |
| `RB_CONFIG_WEB_SEARCH` | `true` | Server-side web search |
| `RB_CONFIG_EXPLORATION` | `true` | Repository exploration tools |
| `RB_CONFIG_TIMEOUT` | `780` | Review timeout in seconds, below the 900s per-PR limit |

ReviewBench runs are ad hoc: a maintainer starts them by hand when
comparing models or strategies. CI never builds the adapter image or runs
it against the corpus, and the image is not published. CI does run the
`internal/reviewbench` unit tests, which use a fake model and need no
credentials.

Build the image and check its output format against the public test set
with a ReviewBench checkout. Each run spends model tokens.

```shell
make reviewbench-image
cd ../ReviewBench
scripts/try-agent.sh ko.local/paco-reviewbench:dev --pr 0 \
  -e GOOGLE_CLOUD_PROJECT -e VERTEX_LOCATION=global \
  -e GOOGLE_APPLICATION_CREDENTIALS=/var/run/secrets/vertex/credentials.json \
  -e RB_CONFIG_STRATEGY=verified
```

`try-agent.sh` checks only the findings format, and it forwards
environment variables but mounts no credential files. Before running the
example above with Vertex AI, resolve the credentials path from the directory
it is relative to and check that it is a readable file:

```shell
export VERTEX_CREDS="$(realpath -e "$GOOGLE_APPLICATION_CREDENTIALS")"
test -f "$VERTEX_CREDS" && test -r "$VERTEX_CREDS"
```

Stop if either check fails. Add
`--mount "type=bind,source=$VERTEX_CREDS,target=/var/run/secrets/vertex/credentials.json,readonly"`
to the `docker run` in a local copy of the script. Unlike `-v`, this fails
when the source is missing rather than creating a directory. Alternatively,
use `-e ANTHROPIC_API_KEY`. Scoring needs the ReviewBench judge.
`hack/paco-reviewbench/configs/` has manifests for both strategies.

For diagnostics, pass `-e RB_DIAGNOSTICS=/work/out/diagnostics.json`.
This writes a separate, scrubbed JSON file containing the review summary,
outcome, context limitations, effective model request settings, prompt digest,
elapsed time and budget counters. Verified runs also include the available
verification counts, dispositions and failure reason. Counts measure actual requests and tool
calls, not which files were read; tokens are provider-reported. A security
block withholds the summary and verification details. Diagnostic write errors
fail the run explicitly.

Diagnostics are off by default. They never retain the temporary workspace,
source snapshots, prompts, raw responses or tool results. Treat summaries as
repository-derived data even after credential redaction. The sidecar is
host-readable, like findings, so keep the parent output directory private.
Use a separate output directory per configuration and copy the sidecar and
logs before the harness replaces its per-PR output on another run.
Do not use the same path for `RB_OUT` and `RB_DIAGNOSTICS`.

To investigate response-format failures, explicitly set
`RB_CAPTURE_RESPONSES=/work/out/responses.json` alongside `RB_DIAGNOSTICS`.
This separate, temporary capture contains final completion text only, with
credential redaction; responses matching the secret scanner are withheld
entirely. It contains no prompts or tool results. Keep its parent directory
private, delete captures after diagnosis, and never commit them. Capture
write failures fail the run. Use a different path from findings and diagnostics.

When comparing settings, keep the checkout, model and prompt version fixed.
Compare source-supported findings and false positives, not just finding counts.
A format pass is not a quality score, and one PR cannot establish a better
default. Live comparisons remain ad hoc and spend model tokens; CI uses fakes.

## Releasing

Pushing a tag builds a binary release through the existing Tekton pipeline
with [GoReleaser](https://goreleaser.com/):

```shell
git tag v0.1.0
git push origin v0.1.0
```

Local snapshot build:

```shell
goreleaser build --snapshot --clean
```

### Container images

`.github/workflows/publish-image.yaml` uses [ko](https://ko.build/) to
publish `ghcr.io/pipelines-as-code/paco-cli` for Linux amd64 and arm64.
It runs on pushes to `main` and `v*` tags, independently of binary releases.
It does not build or publish images for pull requests.

| Push | Image tags |
|---|---|
| `main` | `latest`, `sha-<full-commit>` |
| `v*` tag | Matching tag, `sha-<full-commit>` |

Release pushes do not change `latest`. The examples track main through
`latest`; use their `image` parameter to choose a release tag or digest.
The workflow uses `GITHUB_TOKEN` with `packages: write`, so it needs no
separate registry secret. After the first publication, a maintainer must
make the package public if it starts private. Verify an anonymous pull
before using the examples in another cluster.

`.ko.yaml` builds `cmd/paco` and the ReviewBench adapter, uses vendored
dependencies, and pins the base images by digest. The workflow publishes
only `cmd/paco`. Update the digests when refreshing the base images. `PACO_VERSION` overrides the version embedded in the binary;
local builds default to `dev-<short-commit>`. The commit and date fields
identify the source commit.

Build both platforms into a local OCI layout without publishing:

```shell
KO_DOCKER_REPO=ghcr.io/pipelines-as-code/paco-cli \
  ko build --bare --push=false --oci-layout-path /tmp/paco-oci ./cmd/paco
```

With a running Docker daemon, build a local image for your host platform
and exercise the entrypoint and the command path used by Tekton:

```shell
ko build --local --platform=linux/amd64 --tags=dev ./cmd/paco
image=ko.local/github.com/pipelines-as-code/paco-cli/cmd/paco:dev
docker run --rm "$image" version
docker run --rm --entrypoint /ko-app/paco "$image" diff --help
docker run --rm --entrypoint /ko-app/paco "$image" review --help
docker run --rm --entrypoint /ko-app/paco "$image" post --help
```

Use `--platform=linux/arm64` on an arm64 host. The image runs as UID
65532 and needs no shell. Both PipelineRun examples set `fsGroup: 65532`
to make their shared emptyDir writable; restricted clusters may require
a different filesystem group.
