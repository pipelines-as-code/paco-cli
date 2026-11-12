# Skills

A skill is a short Markdown guide that tells Paco how to investigate a
kind of change, such as a failing PipelineRun. Skills live in the
repository under `.tekton/ai/skills/<name>/SKILL.md` and are read from
the target base revision only. A PR cannot add or change a skill it is
reviewed with. See [Security design](security.md) for the trust model.

## File format

```markdown
---
name: diagnose-pipelinerun-failure
description: Investigate a Pipelines-as-Code PipelineRun failure from the pull request change alone.
---

Guidance for the investigation, in Markdown.
```

- `name` matches the directory name: lowercase letters, digits and single
  hyphens, at most 64 bytes.
- `description` is a single line of at most 1,024 bytes. Claude uses it
  to decide whether to load the skill.
- The body must not be empty. The whole file is at most 12 KiB of UTF-8
  and cannot contain a private key.
- Other frontmatter keys are ignored. Only `SKILL.md` is read; scripts and
  reference files next to it are not available to Paco.
- At most 20 skills are read.

[`cli-contract.md`](cli-contract.md#skills) lists every validation rule.

## Using skills

Name up to three skills after the mode in the trigger comment:

```text
/paco review skill:diagnose-pipelinerun-failure
```

Requested skills are placed in full in the prompt. Every other valid skill
is offered as a catalog of names and descriptions, and Claude may call
`load_skill` for one whose description fits the change. Catalog skills
need repository tools, so `--no-exploration` leaves only the requested
ones. A skill changes what Paco investigates, never the response format.

The PipelineRun in `tekton/paco.pipelinerun.yaml` accepts the
`skill:<name>` words in its `on-comment` pattern; a custom pattern has to
allow them too.

If a requested name is malformed, more than three are given, or a
requested skill is missing or invalid on the target branch, the review
fails before the model is called. The summary comment reports the cause:
a missing name also lists the available skills when they are known, while
an invalid or unavailable skill and a malformed trigger state what was
wrong. A broken skill that nobody requested is only logged by `paco diff`
and left out of the catalog. The collected catalog is the
`.paco-skills.json` artifact.

## Limits

Paco collects the PR diff, source snapshots, existing feedback and the
declared toolchains. It does not collect PipelineRun logs, events, pod
status or any other Kubernetes state, and a skill cannot make it. A skill
guides reasoning over what Paco has, and a good one says what evidence
is still missing instead of guessing.

## Example: diagnosing a failed PipelineRun

[`examples/skills/diagnose-pipelinerun-failure`](../examples/skills/diagnose-pipelinerun-failure/SKILL.md)
is a skill for pull requests that change Tekton files. Copy it to
`.tekton/ai/skills/diagnose-pipelinerun-failure/SKILL.md` on your default
branch.

Say a PR renames the `test` task's parameter and its Pipelines-as-Code run
fails. Comment:

```text
/paco review skill:diagnose-pipelinerun-failure
```

The skill has Claude:

1. Find the changed `.tekton/*.yaml` files and read each whole file, not
   only the hunk.
2. List tasks in execution order with their `runAfter` and `when`
   expressions, and note changed params, images, workspaces and
   `taskRef` names.
3. Use `search_code` to find other users of a renamed parameter or task.
4. Compare against the before snapshot when it exists.
5. Report a narrowed cause plus the missing evidence, for example that
   step `test` now runs `make test-e2e` on an image without `make`, and
   that the run logs would confirm it.

The result is a review of the change, not a diagnosis of the actual run. The skill tells Claude not to invent log output, pod
status or exit codes, so confirm the cause in the real PipelineRun logs.
