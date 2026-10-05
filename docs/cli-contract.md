# CLI Contract

## `paco diff`

Fetches the PR diff and existing feedback, writes artifacts for downstream steps.

### Inputs

| Flag | Required | Description |
|---|---|---|
| `--repo` | yes | GitHub repository (`owner/name`) |
| `--pr` | yes | Pull request number |
| `--comment-id` | no | Trigger comment ID for eyes reaction |
| `--workspace` | no | Workspace directory (default `.`) |

### Environment

- `GH_TOKEN`, else `GITHUB_TOKEN` — GitHub token
- `GITHUB_API_URL` — REST API base URL for GitHub Enterprise; the
  GraphQL endpoint is derived from it (`/api/graphql` on the same host)
  unless `GITHUB_GRAPHQL_URL` is set
- `GH_HOST` — GitHub Enterprise hostname, used when `GITHUB_API_URL`
  is unset (REST at `https://<host>/api/v3/`, GraphQL at
  `https://<host>/api/graphql`; for GHE.com `<tenant>.ghe.com` hosts,
  REST at `https://api.<host>/` and GraphQL at `https://api.<host>/graphql`)

All GitHub URLs must use `https`.

A missing token or invalid URL is reported as a skip (`.paco-error`).

### Artifacts Written

| File | Description |
|---|---|
| `.pr.diff` | Redacted PR diff |
| `.valid-lines.json` | Map of `file → {line: true}` for added lines |
| `.existing-inline.json` | Map of `file → {line: true}` for lines with existing trusted comments |
| `.existing-feedback.txt` | Compact digest of existing feedback (max 30KB) |
| `.head_sha` | HEAD commit SHA |
| `.paco-error` | Skip reason (written on early exit) |
| `.tekton/ai/REVIEW.md` | Repository review rules from base branch (if present) |
| `.toolchain-versions` | Language versions declared on the base branch (if any), one `language<TAB>version<TAB>source` line each |

### Toolchain Detection

`paco diff` lists the base branch root once and fetches only the version
files present there. The first file declaring a language wins:

| Language | Files, in priority order |
|---|---|
| Go | `go.mod` (`go` directive) |
| Rust | `rust-toolchain.toml` (`channel`), `rust-toolchain`, `Cargo.toml` (`rust-version`) |
| Python | `.python-version`, `pyproject.toml` (`requires-python`) |
| Node.js | `.nvmrc`, `.node-version`, `package.json` (`engines.node`) |
| Ruby | `.ruby-version` |
| Java | `.java-version` |
| Any of the above | `.tool-versions` (asdf/mise) |

`paco review` uses these declarations as compatibility context, taking
version ranges and version-file changes in the diff into account. It
does not treat the model's training data as proof that newer syntax or
APIs are invalid.

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success (or skip with `.paco-error` written) |
| non-zero | Fatal error |

---

## `paco review`

Runs the LLM review and produces normalized findings.

### Inputs

| Flag | Required | Description |
|---|---|---|
| `--workspace` | no | Workspace directory (default `.`) |
| `--model` | no | Claude model id, sent as is. Empty means `claude-opus-4-6@default` on Vertex AI and `claude-opus-4-6` on the Anthropic API |
| `--reasoning-effort` | no | `low`, `medium`, `high`, `xhigh`, or `max`. Empty means `low`; `none` omits the API effort parameter. Values are trimmed and lowercased |
| `--no-structured-output` | no | Omit the response schema (default `false`); still request JSON and apply parsing, normalization, and secret scanning |

The effort is sent as the Messages API `output_config.effort`. Which
values a model supports is model-specific (for example, Opus 4.6
accepts `max` but not `xhigh`); an unsupported value surfaces as a
normal backend failure.

By default, the review is requested with structured outputs: the request carries
a JSON schema for `.paco-review.json`. Extended thinking and tool use
are never enabled. The response is streamed and only accepted when it
completes normally; a token-limit stop, a refusal, a truncated stream,
or the 900 second timeout produce a failure summary. The deadline includes
Vertex token acquisition; each OAuth request also has a 30-second timeout.

For models without effort support, such as Haiku 4.5, use
`--reasoning-effort none`. To make a plain-text model request instead of
using the structured-output feature, add `--no-structured-output`:

```shell
paco review --workspace /workspace/source \
  --model claude-haiku-4-5@20251001 \
  --reasoning-effort none --no-structured-output
```

The prompt still asks for the same JSON object, but the API no longer
guarantees its shape. Unparseable responses produce failure artifacts.
Paco never retries a rejected schema request without the schema automatically.

### Environment

The backend is picked from the environment:

1. `ANTHROPIC_API_KEY` set — the Anthropic API at
   `https://api.anthropic.com`. Other `ANTHROPIC_*` variables, such as
   `ANTHROPIC_BASE_URL`, are ignored.
2. Otherwise `GOOGLE_APPLICATION_CREDENTIALS` — Vertex AI with the
   service account JSON at that path.
   - `GOOGLE_CLOUD_PROJECT` — Vertex AI project ID (falls back to
     `project_id` in the credentials file)
   - `VERTEX_LOCATION` — Vertex AI location (default `global`)
3. Neither set — the run fails with a failure summary.

Other variables:

- `TRIGGER_COMMENT` — trigger comment text (determines review/summary mode)

### Artifacts Read

- `.pr.diff`, `.paco-error`, `.existing-feedback.txt`, `.tekton/ai/REVIEW.md`, `.toolchain-versions`

### Artifacts Written

| File | Description |
|---|---|
| `.paco-review.json` | Normalized review: summary, review_score, comments (max 30) |
| `.paco-mode` | `review` or `summary` |
| `.paco-failed` | Marker for error/skip path |
| `.paco-security-block` | Rule name that triggered secret withholding |

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success (including fail/withhold paths — check markers) |
| non-zero | Fatal error |

---

## `paco post`

Posts the review results to GitHub.

### Inputs

| Flag | Required | Description |
|---|---|---|
| `--repo` | yes | GitHub repository (`owner/name`) |
| `--pr` | yes | Pull request number |
| `--workspace` | no | Workspace directory (default `.`) |

### Environment

- `GH_TOKEN`, else `GITHUB_TOKEN` — GitHub token
- `GITHUB_API_URL` — REST API base URL for GitHub Enterprise; the
  GraphQL endpoint is derived from it (`/api/graphql` on the same host)
  unless `GITHUB_GRAPHQL_URL` is set
- `GH_HOST` — GitHub Enterprise hostname, used when `GITHUB_API_URL`
  is unset (REST at `https://<host>/api/v3/`, GraphQL at
  `https://<host>/api/graphql`; for GHE.com `<tenant>.ghe.com` hosts,
  REST at `https://api.<host>/` and GraphQL at `https://api.<host>/graphql`)

All GitHub URLs must use `https`.

The resolved token is also used as a literal in the belt-and-braces
secret rescan. A missing token or invalid URL is a fatal error.

### Artifacts Read

- `.paco-review.json`, `.valid-lines.json`, `.existing-inline.json`
- `.head_sha`, `.paco-mode`, `.paco-failed`, `.paco-security-block`

### GitHub Side Effects

1. Create or update the `<!-- paco-review -->` sticky summary comment
2. Create/ensure `paco/review-*` difficulty labels, apply current, remove stale
3. Add `security-review` label when `security_sensitive` is true (never removed)
4. Submit inline review with filtered, deduplicated comments on valid added lines

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success (inline review failure is logged, not fatal) |
| non-zero | Fatal error |
