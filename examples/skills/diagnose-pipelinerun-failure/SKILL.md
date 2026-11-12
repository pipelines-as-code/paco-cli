---
name: diagnose-pipelinerun-failure
description: Investigate a Pipelines-as-Code PipelineRun failure from the pull request change alone, narrowing it to a changed task, step, parameter, image or script.
---

# Diagnosing a failed PipelineRun

Use this skill when a Pipelines-as-Code run failed on this pull request.
Paco cannot see the PipelineRun logs, the events, the pods or the cluster.
You have only the diff, the source snapshots, existing feedback and the
declared toolchains, so narrow the failure to a specific changed line and
say what evidence is still missing. Never state what the logs showed and
never claim a run was executed.

## Track the change into the Pipeline

1. Find the changed pipeline files: `.tekton/*.yaml`, `tekton/**`, any
   `PipelineRun` or `Pipeline` referenced by an annotation.
2. Read the whole file, not only the changed hunk, and list the tasks in
   execution order with their `runAfter` and `when` expressions.
3. Note every changed value that a run resolves at start: `params`,
   `default` values, `workspaces`, `serviceAccountName`, `taskRef` names and
   `image` references.
4. Use `search_code` to find other files that read the same parameter or
   task name; a renamed or retyped parameter breaks every consumer.

## Check the common failure shapes

- A task or step name changed while a `runAfter`, `when` or result
  reference still uses the old name.
- A parameter default changed type (string to array, array to string) or a
  `$(params.x)` reference now resolves to an empty value.
- An image tag or digest changed to one that does not exist, needs a
  different entrypoint, or expects a different shell.
- A script gained `set -e` behaviour, an unquoted variable, a removed
  `|| true`, or lost an environment variable it used to inherit.
- A new path under a workspace is not mounted, or the mount became
  read-only.
- A step now runs as a different user, so a previously writable directory
  is no longer writable.

## Compare with the before snapshot

When the before snapshot is available, read the previous version of the
same file and diff the effective values: parameters, images, commands,
working directories, volumes and secret references. A failure that started
with this pull request is normally a changed value, not a missing feature.

## Report the limits

End the investigation with the narrowed cause and the missing evidence, for
example: "step `test` now runs `make test-e2e` against image `golang:1.24`,
which no longer ships `make`; the run logs would confirm the exec error."
Do not invent log output, pod status, exit codes or event messages.
