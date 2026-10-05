# paco-cli

Go CLI for the Paco AI code reviewer.

Paco reviews GitHub pull requests using an LLM and posts inline
findings, a summary comment, and review-difficulty labels.

## Subcommands

| Command | Description |
|---|---|
| `paco diff` | Fetch PR diff, parse added lines, gather existing feedback |
| `paco review` | Assemble prompt, run LLM review, extract and normalize findings |
| `paco post` | Post sticky summary, labels, and inline review to GitHub |
| `paco version` | Print version, commit, and build date |

## Usage

Each subcommand corresponds to a Tekton step. The shared workspace
directory holds the artifacts passed between steps.

```shell
# Step 1: fetch diff and existing feedback
paco diff --repo owner/repo --pr 42 --workspace /workspace/source

# Step 2: run AI review
paco review --workspace /workspace/source

# Step 3: post results to GitHub
paco post --repo owner/repo --pr 42 --workspace /workspace/source
```

## Flags and Environment

### Flags

| Flag | Subcommand | Required | Description |
|---|---|---|---|
| `--repo` | `diff`, `post` | yes | GitHub repository (`owner/name`) |
| `--pr` | `diff`, `post` | yes | Pull request number |
| `--comment-id` | `diff` | no | Trigger comment ID (for eyes reaction) |
| `--workspace` | all | no | Workspace directory (default `.`) |
| `--model` | `review` | no | Claude model id (default `claude-opus-4-6@default` on Vertex AI, `claude-opus-4-6` on the Anthropic API) |
| `--reasoning-effort` | `review` | no | `low`, `medium`, `high`, `xhigh`, or `max` (default `low`); `none` omits the API effort parameter |
| `--no-structured-output` | `review` | no | Omit the API response schema; JSON parsing and secret checks remain enabled (default `false`) |

### Environment Variables

| Variable | Subcommand | Description |
|---|---|---|
| `GH_TOKEN` | `diff`, `post` | GitHub token |
| `GITHUB_TOKEN` | `diff`, `post` | GitHub token, used when `GH_TOKEN` is unset |
| `GITHUB_API_URL` | `diff`, `post` | GitHub REST API base URL for GitHub Enterprise (for example `https://ghe.example.com/api/v3`) |
| `GH_HOST` | `diff`, `post` | GitHub Enterprise Server or GHE.com hostname, used when `GITHUB_API_URL` is unset |
| `GITHUB_GRAPHQL_URL` | `diff` | GraphQL endpoint, when it cannot be derived from `GITHUB_API_URL` |
| `ANTHROPIC_API_KEY` | `review` | Anthropic API key; when set, Paco calls the Anthropic API instead of Vertex AI |
| `GOOGLE_APPLICATION_CREDENTIALS` | `review` | Path to the Vertex AI service account JSON |
| `GOOGLE_CLOUD_PROJECT` | `review` | Vertex AI project ID (default: `project_id` from the service account JSON) |
| `VERTEX_LOCATION` | `review` | Vertex AI location (default `global`) |
| `TRIGGER_COMMENT` | `review` | Trigger comment text (determines review vs summary mode) |

## Integration with Pipelines-as-Code

Paco is designed to run as a Tekton PipelineRun triggered by
[Pipelines-as-Code](https://pipelinesascode.com). A full example is
in [`examples/pipelinerun.yaml`](examples/pipelinerun.yaml).

### Prerequisites

1. **GitHub token** — Pipelines-as-Code provides this automatically
   via `{{git_auth_secret}}`.

2. **Model credentials**, one of:

   - Vertex AI: a Kubernetes secret with your Google Cloud service
     account key, used by
     [`examples/pipelinerun.yaml`](examples/pipelinerun.yaml):

     ```shell
     kubectl create secret generic paco-vertex-credentials \
       --from-file=service-account.json=/path/to/service-account.json
     ```

   - Anthropic API: a Kubernetes secret with your API key, used by
     [`examples/pipelinerun-anthropic.yaml`](examples/pipelinerun-anthropic.yaml):

     ```shell
     kubectl create secret generic paco-anthropic-api-key \
       --from-literal=api-key=sk-ant-...
     ```

### Setup

1. Copy [`examples/pipelinerun.yaml`](examples/pipelinerun.yaml)
   (Vertex AI) or
   [`examples/pipelinerun-anthropic.yaml`](examples/pipelinerun-anthropic.yaml)
   (Anthropic API) to `.tekton/paco.yaml` in your repository.

2. Update the `CHANGEME` values (image, GCP project, secret names).

3. Optionally add review rules at `.tekton/ai/REVIEW.md` — see
   [`examples/review-rules.md`](examples/review-rules.md) for the
   format. Paco loads these from the base branch so a PR cannot weaken
   its own review criteria.

4. Paco also reads the language versions declared on the base branch
   (`go.mod`, `.python-version`, `package.json` engines, and similar)
   and reviews code against them. See the
   [CLI contract](docs/cli-contract.md#toolchain-detection) for the
   supported files.

### Triggers

| Trigger | Behavior |
|---|---|
| PR opened/reopened against `main` | Automatic full review |
| `/paco review` comment | On-demand full review |
| `/paco summary` comment | On-demand summary only |

## Requirements

Paco is a single static binary. It talks to the GitHub API and to
Claude (on Vertex AI or the Anthropic API) over HTTPS, so the container
image only needs `paco` and CA certificates. No shell, `gh`, or other
CLI is required.

## Installation

From a [GitHub release](https://github.com/pipelines-as-code/paco-cli/releases):

```shell
curl -L https://github.com/pipelines-as-code/paco-cli/releases/latest/download/paco_linux_amd64.tar.gz | tar xz -C /usr/local/bin paco
```

From source:

```shell
make build
# binary at bin/paco
```

## Documentation

- [CLI contract](docs/cli-contract.md) — inputs, artifacts, exit codes
- [Security design](docs/security.md) — trust boundaries, redaction, scanning
- [Development guide](docs/development.md) — build, test, contribute

## Links

- [Design issue](https://github.com/tektoncd/pipelines-as-code/issues/2865)
- [Pipelines-as-Code](https://github.com/openshift-pipelines/pipelines-as-code)

## License

[Apache License 2.0](LICENSE)
