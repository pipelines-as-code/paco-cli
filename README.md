# paco-cli

Paco reviews GitHub pull requests with Claude. It posts inline
findings, a summary comment, and a review-difficulty label.

## Usage

Paco runs as three Tekton steps that share a workspace directory:

```shell
paco diff   --repo owner/repo --pr 42 --workspace /workspace/source
paco review --workspace /workspace/source
paco post   --repo owner/repo --pr 42 --workspace /workspace/source
```

- `paco diff` fetches the diff, existing feedback, base-branch review
  rules and toolchain versions, and a source snapshot of the PR head.
- `paco review` builds the prompt, calls Claude, and normalizes the
  findings.
- `paco post` updates the summary comment, labels, and inline review.
- `paco version` prints the build version.

`diff` and `post` read the GitHub token from `GH_TOKEN` or
`GITHUB_TOKEN`. `review` uses the Anthropic API when
`ANTHROPIC_API_KEY` is set, otherwise Vertex AI with
`GOOGLE_APPLICATION_CREDENTIALS`.

During review, Claude can read files from the PR-head snapshot and run
web searches for public library documentation. Pass `--no-exploration`
or `--web-search=false` to turn these off.

The [CLI contract](docs/cli-contract.md) lists every flag, environment
variable, artifact, and limit.

## Pipelines-as-Code setup

1. Copy [`examples/pipelinerun.yaml`](examples/pipelinerun.yaml)
   (Vertex AI) or
   [`examples/pipelinerun-anthropic.yaml`](examples/pipelinerun-anthropic.yaml)
   (Anthropic API) to `.tekton/paco.yaml` and update the `CHANGEME`
   values.

2. Create the model credentials secret:

   ```shell
   # Vertex AI
   kubectl create secret generic paco-vertex-credentials \
     --from-file=service-account.json=/path/to/service-account.json

   # Anthropic API
   kubectl create secret generic paco-anthropic-api-key \
     --from-literal=api-key=sk-ant-...
   ```

   Pipelines-as-Code provides the GitHub token through
   `{{git_auth_secret}}`.

3. Optionally add review rules at `.tekton/ai/REVIEW.md` (see
   [`examples/review-rules.md`](examples/review-rules.md)). Paco reads
   them from the base branch, so a PR cannot weaken its own rules.

Paco reviews PRs opened or reopened against `main`. Comment
`/paco review` for a new review or `/paco summary` for a summary only.

## Installation

Download a [release](https://github.com/pipelines-as-code/paco-cli/releases):

```shell
curl -L https://github.com/pipelines-as-code/paco-cli/releases/latest/download/paco_linux_amd64.tar.gz | tar xz -C /usr/local/bin paco
```

Or build from source with `make build` (output in `bin/paco`).

Paco is a static binary. The container image only needs `paco` and CA
certificates.

## Documentation

- [CLI contract](docs/cli-contract.md): flags, environment, artifacts, exit codes
- [Security design](docs/security.md): trust boundaries, redaction, scanning
- [Development guide](docs/development.md): build, test, release
- [Design issue](https://github.com/tektoncd/pipelines-as-code/issues/2865)

## License

[Apache License 2.0](LICENSE)
