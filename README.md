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
| `--model` | `review` | no | Model identifier (default `google-vertex-anthropic/claude-sonnet-5@default`) |

### Environment Variables

| Variable | Subcommand | Description |
|---|---|---|
| `GH_TOKEN` | `diff`, `post` | GitHub token (or use `gh` CLI auth) |
| `GOOGLE_APPLICATION_CREDENTIALS` | `review` | Path to Vertex AI service account JSON |
| `GOOGLE_CLOUD_PROJECT` | `review` | Vertex AI project ID |
| `VERTEX_LOCATION` | `review` | Vertex AI location (default `global`) |
| `TRIGGER_COMMENT` | `review` | Trigger comment text (determines review vs summary mode) |

## Integration with Pipelines-as-Code

Paco is designed to run as a Tekton PipelineRun triggered by
[Pipelines-as-Code](https://pipelinesascode.com). A full example is
in [`examples/pipelinerun.yaml`](examples/pipelinerun.yaml).

### Prerequisites

1. **GitHub token** — Pipelines-as-Code provides this automatically
   via `{{git_auth_secret}}`.

2. **Vertex AI credentials** — create a Kubernetes secret with your
   Google Cloud service account key:

   ```shell
   kubectl create secret generic paco-vertex-credentials \
     --from-file=service-account.json=/path/to/service-account.json
   ```

### Setup

1. Copy [`examples/pipelinerun.yaml`](examples/pipelinerun.yaml) to
   `.tekton/paco.yaml` in your repository.

2. Update the `CHANGEME` values (GCP project, secret names).

3. Optionally add review rules at `.tekton/ai/REVIEW.md` — see
   [`examples/review-rules.md`](examples/review-rules.md) for the
   format. Paco loads these from the base branch so a PR cannot weaken
   its own review criteria.

### Triggers

| Trigger | Behavior |
|---|---|
| PR opened/reopened against `main` | Automatic full review |
| `/paco review` comment | On-demand full review |
| `/paco summary` comment | On-demand summary only |

## Requirements

- `gh` (GitHub CLI) — authenticated and on PATH
- `opencode` — for the model call (review step only)

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
