# paco-cli

Go CLI for the Paco AI code reviewer.

Paco reviews GitHub pull requests using an LLM and posts inline findings.
This CLI replaces the bash/jq/awk/Node.js scripts in the
[Pipelines-as-Code](https://github.com/openshift-pipelines/pipelines-as-code)
`.tekton/paco.yaml` pipeline with a testable, reviewable Go binary.

## Status

Under development. See [pipelines-as-code#2865](https://github.com/tektoncd/pipelines-as-code/issues/2865) for the full design.

## License

[Apache License 2.0](LICENSE)
