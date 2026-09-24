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

- `GH_TOKEN` or `gh` CLI auth — GitHub access

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
| `--model` | no | Model identifier (default `google-vertex-anthropic/claude-sonnet-5@default`) |
| `--reasoning-effort` | no | Reasoning effort: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. Empty means `minimal`. Values are trimmed and lowercased |

Passed to opencode as the `paco-reviewer` agent's `variant`. Which values a
given model actually supports is model-specific; an unsupported one surfaces as
a normal backend failure.

Verified against opencode `1.18.31`, the version shipped in
`ghcr.io/chmouel/agents-image`. Note that `--variant` is not a valid
`opencode run` flag, and a `#variant` suffix on `--model` breaks model
resolution on that version.

### Environment

- `GOOGLE_APPLICATION_CREDENTIALS` — path to Vertex AI service account JSON
- `GOOGLE_CLOUD_PROJECT` — Vertex AI project ID (falls back to credentials file)
- `VERTEX_LOCATION` — Vertex AI location (default `global`)
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

- `GH_TOKEN` or `gh` CLI auth — GitHub access (rescanned for belt-and-braces secret check)

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
