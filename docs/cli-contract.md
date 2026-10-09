# CLI Contract

## `paco diff`

Fetches the PR diff and existing feedback and writes artifacts for the next steps.

### Inputs

| Flag | Required | Description |
|---|---|---|
| `--repo` | yes | GitHub repository (`owner/name`) |
| `--pr` | yes | Pull request number |
| `--comment-id` | no | Trigger comment ID for eyes reaction |
| `--workspace` | no | Workspace directory (default `.`) |

### Environment

- `GH_TOKEN`, else `GITHUB_TOKEN`: GitHub token
- `GITHUB_API_URL`: REST API base URL for GitHub Enterprise. The
  GraphQL endpoint is derived from it (`/api/graphql` on the same host)
  unless `GITHUB_GRAPHQL_URL` is set.
- `GH_HOST`: GitHub Enterprise hostname, used when `GITHUB_API_URL`
  is unset. REST is at `https://<host>/api/v3/` and GraphQL at
  `https://<host>/api/graphql`. For GHE.com (`<tenant>.ghe.com`), REST
  is at `https://api.<host>/` and GraphQL at `https://api.<host>/graphql`.

All GitHub URLs must use `https`.

A missing token or invalid URL is reported as a skip (`.paco-error`).

### Artifacts Written

| File | Description |
|---|---|
| `.pr.diff` | Redacted PR diff |
| `.valid-lines.json` | `{file: {line: true}}` for lines shown in the diff on the head side (added and context) |
| `.existing-inline.json` | `{file: {line: true}}` for lines with existing trusted comments |
| `.existing-feedback.txt` | Compact digest of existing feedback (max 30KB) |
| `.existing-feedback.json` | Bounded trusted inline feedback with run-local IDs and availability status |
| `.head_sha` | HEAD commit SHA |
| `.paco-error` | Skip reason (written on early exit) |
| `.tekton/ai/REVIEW.md` | Repository review rules from base branch (if present) |
| `.toolchain-versions` | Language versions declared on the base branch (if any), one `language<TAB>version<TAB>source` line each |
| `.paco-source.json` | Redacted text-file snapshot pinned to `.head_sha`, for read-only exploration |
| `.paco-source-before.json` | Optional comparison-side source snapshot |
| `.paco-input.json` | Versioned PR/comparison identities, diff digest, and context availability |

The source snapshot is optional. If collection fails or hits a limit,
`diff` logs a warning and writes none. Any snapshot from an earlier run
is deleted first.

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

Runs the model review and writes normalized findings.

### Inputs

| Flag | Required | Description |
|---|---|---|
| `--workspace` | no | Workspace directory (default `.`) |
| `--model` | no | Bare Claude model id, sent as is; ids with a provider prefix such as `google-vertex-anthropic/` are rejected. Empty means `claude-opus-4-6@default` on Vertex AI and `claude-opus-4-6` on the Anthropic API |
| `--reasoning-effort` | no | `low`, `medium`, `high`, `xhigh`, or `max`. Empty means `low`; `none` omits the API effort parameter. Values are trimmed and lowercased |
| `--no-structured-output` | no | Omit the API response schema (default `true`) |
| `--no-exploration` | no | Disable repository tools (default `false`) |
| `--web-search` | no | Allow web search for public library docs (default `true`) |
| `--verify-findings` | no | Discover and independently verify evidence-backed findings (default `false`) |

The effort is sent as `output_config.effort`. Supported values depend
on the model (Opus 4.6 accepts `max` but not `xhigh`); an unsupported
value fails like any other backend error. Use `--reasoning-effort none`
for models without effort support, such as Haiku 4.5:

```shell
paco review --workspace /workspace/source \
  --model claude-haiku-4-5@20251001 \
  --reasoning-effort none
```

The prompt always asks for the review JSON. By default the API does not
enforce a schema, because web search is incompatible with it. Pass
`--web-search=false --no-structured-output=false` to send the schema.
Either way, Paco parses, normalizes, and secret-scans the output, and
unparsable output produces failure artifacts.

The response is streamed with extended thinking disabled. A token-limit
stop, refusal, truncated stream, or the 900-second timeout produces a
failure summary. The timeout includes Vertex token acquisition, and each
OAuth request has its own 30-second timeout. Paco never retries
automatically.

### Repository Exploration

When `.paco-source.json` is present and matches `.head_sha`, `review`
exposes `list_files`, `read_file`, and `search_code` to Claude. Reads
take 1-based line ranges. Searches are literal and case-sensitive, with
an optional path substring filter. A corrupt or mismatched snapshot
fails the review; a missing one falls back to a diff-only review.

When a snapshot is available and the diff touches `.go` files, the prompt
also carries a change inventory: each changed line is mapped to its
enclosing top-level declaration using Go's standard parser, with line
ranges and hunk numbers. Verified mode maps both revisions; single-pass
has only the head snapshot, so deleted declarations are not listed there.
Parse failures, missing files and truncation are stated in the inventory.
The diff stays authoritative for anchors.

`diff` builds the snapshot from a tarball of the PR head, keeping
regular UTF-8 text files in memory without extracting them to disk. It
skips symlinks, hardlinks, binary files, files over 512 KiB, common
credential files, and dependency directories such as `vendor` and
`node_modules`.

Limits:

- 32 MiB downloaded archive, 128 MiB expanded archive.
- 16 MiB source text, 10,000 retained files, 32 MiB encoded snapshot.
- 200 lines per read, 100 search/list results, under 16,000 bytes per tool result.
- 80 repository tool calls, 6 web searches, 24 model turns. These keep a
  runaway loop finite; the review deadline and token limits bound cost.

The last allowed turn has no tools: Paco sends `tool_choice: none` and
asks Claude to return the review using only findings it confirmed. When
Claude asks for more repository calls than remain, the extra calls get
an error result and the next turn is that final turn. Truncated tool
results say so. A tool request on the final turn still produces a
failure artifact.

Web search uses the provider's `web_search_20250305` tool, which may
cost extra. Claude is told to search only public package names and
versions and to cite URLs for findings that rely on web results. Search
errors fail the review.

### Verified Findings

`--verify-findings` selects discovery followed by a fresh verification
conversation. Single-pass review remains the default. Summary-only requests
skip verification and cannot produce inline findings.

Verified mode requires fresh `.paco-input.json` metadata from `diff`. It checks
the diff digest and head identity, rejects incomplete hunks, and validates
snapshot lines against overlapping diff lines. Head and before snapshots share
the retained-source limits. The before revision is the comparison merge base;
trusted rules come from the target base revision.

Candidates anchor on a line the diff shows, including unchanged context lines
in a hunk, and specify a triggering condition, impact, remedy, and source
quotes; the evidence still has to include a changed line of the file. Local checks reject invented paths, ranges, quotes and changed-line
anchors. The verifier looks for counterevidence and returns one decision per
candidate; an accepted decision carries the published severity, set from the
confirmed impact. A valid citation is not proof of a bug; model judgment is
still required. Prior trusted feedback supports issue-level deduplication. Different
issues on the same line may both be published.

Verified-mode diff context and file/search results encode source lines as
`source_json` strings, making tabs and spaces explicit. The model decodes those
strings when citing evidence. Local validation compares the quoted lines with
the source ignoring indentation, trailing whitespace and runs of blanks; every
other character has to match. A discovery candidate whose only defect is a
misquoted line keeps its anchor and goes to the verifier without evidence,
which must then cite the source itself. Single-pass source tools retain their
existing text format.

Both passes share the 900-second deadline and total allowance of 24 turns,
80 repository calls, and six web searches. Discovery gets at most 16 turns,
60 repository calls and four searches; verification can use the remainder.
Each response retains the 16,384-output-token limit. Total token cost can
increase even though call limits are shared. With no viable candidates,
Paco skips the verifier.

In addition to revision-selectable source reads, `read_diff` exposes bounded
numbered hunks. Missing before context falls back to the diff and is reported
as incomplete coverage. Failed tool reads and truncated results also mark the
run partial. Budget exhaustion, malformed verifier output, provider errors,
or invalid verifier citations withhold all findings.

Plain-output responses may put the final object in a `json` code fence, which
may be left unclosed, or leave it bare. Any commentary before it is allowed,
including brackets and earlier draft objects: the last complete object is the
answer. Trailing commentary after the object is rejected.
The extracted object still has to pass every schema and evidence check.

`.paco-status.json` records versioned provenance, usage, limitations, candidate
outcomes, publication counts and a scrubbed `failure_reason` when verification
fails. Candidate outcomes are also printed in the review log, and the summary
comment lists unpublished candidates with their outcome and reason. Verified `.paco-review.json` output can include
`summary_findings` for deletion-side findings without an added-line anchor.
Raw model transcripts are not persisted. Review output, mode, failure,
security-block and status artifacts are cleared before each review attempt.

`post` requires matching status metadata for verified output, checks the
repository/PR/head identity, and refuses publication if the PR head changed.
Deletion-side findings appear in the summary. Publication failure returns an
error. A saved successful publication count prevents duplicate inline
submission on a repeat of the same workspace; this does not guarantee
exactly-once delivery across network failures or different workspaces.

### Environment

The backend is picked from the environment:

1. `ANTHROPIC_API_KEY` set: the Anthropic API at
   `https://api.anthropic.com`. Other `ANTHROPIC_*` variables, such as
   `ANTHROPIC_BASE_URL`, are ignored.
2. Otherwise `GOOGLE_APPLICATION_CREDENTIALS`: Vertex AI with the
   service account JSON at that path.
   - `GOOGLE_CLOUD_PROJECT`: Vertex AI project ID (falls back to
     `project_id` in the credentials file)
   - `VERTEX_LOCATION`: Vertex AI location (default `global`)
3. Neither set: the run fails with a failure summary.

Other variables:

- `TRIGGER_COMMENT`: trigger comment text. `/paco summary` selects summary mode.

### Artifacts Read

- `.pr.diff`, `.paco-error`, `.existing-feedback.txt`, `.tekton/ai/REVIEW.md`, `.toolchain-versions`
- `.paco-source.json`, `.head_sha` when repository exploration is enabled

### Artifacts Written

| File | Description |
|---|---|
| `.paco-review.json` | Normalized review: summary, review_score, comments (max 30) |
| `.paco-mode` | `review` or `summary` |
| `.paco-failed` | Marker for error/skip path |
| `.paco-security-block` | Rule name that triggered secret withholding |
| `.paco-status.json` | Verified-mode identity, output digest, outcomes, limitations and usage |

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success, including the fail and withhold paths (check the marker files) |
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

Same as [`paco diff`](#environment).

`post` also rescans the review for its own token. A missing token or
invalid URL is a fatal error.

### Artifacts Read

- `.paco-review.json`, `.valid-lines.json`, `.existing-inline.json`
- `.head_sha`, `.paco-mode`, `.paco-failed`, `.paco-security-block`

### GitHub Side Effects

1. Create or update the `<!-- paco-review -->` sticky summary comment
2. Create/ensure `paco/review-*` difficulty labels, apply current, remove stale
3. Add `security-review` label when `security_sensitive` is true (never removed)
4. Submit inline review with filtered, deduplicated comments on valid diff lines

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success (legacy inline review failure is logged; verified-mode failure is fatal) |
| non-zero | Fatal error |
