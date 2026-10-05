# Security Design

## Trust Boundaries

- **Diff content** is untrusted: PRs can contain anything, including
  prompt injection attempts and leaked credentials. The diff is redacted
  before it reaches the model.

- **Existing feedback** is filtered by write access. Only comments from
  users with `write`, `admin`, or `maintain` permission are included.
  Resolved threads and dismissed reviews are excluded.

- **Review rules** (`.tekton/ai/REVIEW.md`) are loaded from the base
  branch only, never from the PR head. A PR cannot weaken its own
  review rules.

- **Toolchain versions** (`.toolchain-versions`) come from version files
  on the base branch only, such as `go.mod` or `.python-version`. Both
  `paco diff` and `paco review` drop any entry whose language, source
  file, or version string does not match the known detectors. Values
  matching credential patterns are also dropped. Optional artifacts
  from earlier runs are removed before fetching the current base branch.

- **Model output** is untrusted. It passes through secret scanning
  before any GitHub write.

## Redaction

Credential-shaped strings are redacted before writing the diff to the
workspace and before logging any output. Patterns:

- GitHub tokens: `ghp_`, `gho_`, `ghs_`, `ghu_`, `ghr_` prefixed
- GitHub PATs: `github_pat_` prefixed
- JWTs: `eyJ...` base64 header pairs
- AWS access keys: `AKIA` prefixed
- GCP service account emails: `*@*.iam.gserviceaccount.com`
- PEM private key headers
- Anthropic API keys: `sk-ant-` prefixed

## Secret Scanning

Model output is scanned before any GitHub write. If a credential
pattern is found:

1. The review step writes `.paco-security-block` with the rule name
2. The post step sees the block and posts a withhold notice
3. No inline comments are posted

The review step also scans for its active model credentials (the
Anthropic API key or the service-account email). The post step
rescans independently using its own GitHub token as an extra literal
match (belt-and-braces).

## External Services

Paco runs no subprocesses. It calls two HTTPS APIs directly:

- GitHub, through `internal/ghclient` (go-github for REST, one
  GraphQL query for review threads).
  - The token is read from `GH_TOKEN` or `GITHUB_TOKEN` and is only
    sent to the configured REST and GraphQL origins.
  - Non-`https` URLs and URLs with embedded credentials are rejected.
  - Redirects to another origin or from `https` to `http` are refused.
- Claude, through `internal/model` (anthropic-sdk-go).
  - Credentials are passed explicitly. The SDK does not load
    `ANTHROPIC_BASE_URL`, profile files, or Google application default
    credentials, so the environment cannot redirect review content.
  - Requests are not retried automatically, and the same redirect
    rules apply.
  - Tool use and extended thinking are never enabled. The model only
    is asked to return the review JSON. The API enforces its schema
    unless `--no-structured-output` is passed; local parsing,
    normalization, and secret scanning remain enabled in either mode.
- Errors from both services are scrubbed of the active credentials,
  redacted, and truncated before logging.

## Output Limits

- PR diff: 200KB max
- Feedback digest: 30KB max
- Inline comments: 30 max per review
- Review score rating: clamped to 1-5
- Severity: clamped to `critical`, `high`, `medium`, `low`
