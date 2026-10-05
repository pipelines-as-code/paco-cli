# Security Design

## Trust Boundaries

The PR diff is untrusted. It can contain prompt injection and leaked
credentials, so Paco redacts it before the model sees it.

Existing feedback only includes comments from users with `write`,
`admin`, or `maintain` permission. Resolved threads and dismissed
reviews are dropped.

Review rules (`.tekton/ai/REVIEW.md`) and toolchain versions come from
the base branch only, so a PR cannot change them. Both `diff` and
`review` drop toolchain entries that do not match a known detector or
that look like credentials. `diff` deletes these files from earlier
runs before fetching the current base branch.

Model output is untrusted and is secret-scanned before any GitHub write.

The source snapshots and web results are untrusted too. The model reads
source only through bounded reads of the head and comparison-merge-base
snapshots tied to the reviewed diff; it cannot run code, run tests, or read other
workspace files. Archive entries stay in memory, and links and unsafe
paths are skipped or rejected. The snapshot is redacted for credential
patterns and the GitHub token when collected, and for model credentials
when loaded.

Verified review checks source quotes, changed-line references, and
snapshot/diff consistency before a separate model pass judges each candidate.
Intermediate model outputs are secret-scanned before reuse. The verifier
uses the same configured model in a fresh conversation; agreement between
the passes is not proof of correctness.

The status artifact binds the final review bytes to its PR and head revision.
It detects stale or mismatched artifacts, not deliberate forgery by an attacker
who can modify the whole workspace. `post` rechecks the current PR head, but
GitHub operations are not atomic across summary and inline writes.

## Redaction

Credential-shaped strings are redacted before writing the diff to the
workspace and before logging. Patterns:

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

The review step also checks for its own model credential (the
Anthropic API key or the service-account email). The post step
rescans the review and also checks for its own GitHub token.

## External Services

Paco runs no subprocesses. It calls two HTTPS APIs directly:

- GitHub, through `internal/ghclient` (go-github for REST, one
  GraphQL query for review threads).
  - The token is read from `GH_TOKEN` or `GITHUB_TOKEN` and is only
    sent to the configured REST and GraphQL origins.
  - Non-`https` URLs and URLs with embedded credentials are rejected.
  - Redirects to another origin or from `https` to `http` are refused.
  - Archive retrieval uses the download URL returned by GitHub. The
    GitHub token is not forwarded to a separate codeload origin, and
    subsequent cross-origin redirects are refused. Signed download URLs
    are not printed in errors.
- Claude, through `internal/model` (anthropic-sdk-go).
  - Credentials are passed explicitly. The SDK does not load
    `ANTHROPIC_BASE_URL`, profile files, or Google application default
    credentials, so the environment cannot redirect review content.
  - Requests are not retried automatically, and the same redirect
    rules apply.
  - Only read-only snapshot tools and basic web search are exposed.
    Shells, code execution, dynamic filtering, and extended thinking are
    disabled.

The review step scrubs known model credentials and credential patterns
from diagnostic output and truncates it to 4,000 bytes.

Web search is on by default, and its queries go to the provider's
search service. The system prompt forbids source snippets, credentials,
private identifiers, and internal URLs in queries, but nothing enforces
that. Use `--web-search=false` if that risk is not acceptable.

## Output Limits

- PR diff: 200KB max
- Feedback digest: 30KB max
- Inline comments: 30 max per review
- Review score rating: clamped to 1-5
- Severity: clamped to `critical`, `high`, `medium`, `low`
