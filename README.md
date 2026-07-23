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
