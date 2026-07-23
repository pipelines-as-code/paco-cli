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

### Environment

- `GOOGLE_APPLICATION_CREDENTIALS` — path to Vertex AI service account JSON
- `GOOGLE_CLOUD_PROJECT` — Vertex AI project ID (falls back to credentials file)
- `VERTEX_LOCATION` — Vertex AI location (default `global`)
- `TRIGGER_COMMENT` — trigger comment text (determines review/summary mode)

### Artifacts Read

- `.pr.diff`, `.paco-error`, `.existing-feedback.txt`, `.tekton/ai/REVIEW.md`

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
