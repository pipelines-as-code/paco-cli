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

## Secret Scanning

Model output is scanned before any GitHub write. If a credential
pattern is found:

1. The review step writes `.paco-security-block` with the rule name
2. The post step sees the block and posts a withhold notice
3. No inline comments are posted

The post step rescans independently using its own `GH_TOKEN` as an
extra literal match (belt-and-braces).

## Subprocess Execution

- All external commands (`gh`, `opencode`) run through the
  `command.Runner` interface with separate arguments — never via
  `sh -c`.
- `opencode` runs with a sanitized environment (`env -i` equivalent):
  only `HOME`, `PATH`, `TERM`, `NO_COLOR`, and Vertex credentials
  are passed.
- All tools are denied in the OpenCode config (`permission: deny`).
- Model output is bounded and redacted before logging.

## Output Limits

- PR diff: 200KB max
- Feedback digest: 30KB max
- Inline comments: 30 max per review
- Review score rating: clamped to 1-5
- Severity: clamped to `critical`, `high`, `medium`, `low`
