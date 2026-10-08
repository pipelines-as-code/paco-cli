# ADR 0001: Go-aware change inventory

## Status

Proposed. This record does not approve implementation or change review defaults.

## Context

Paco exposes file reads, literal searches, and numbered diff hunks through
[`internal/source`](../../internal/source/toolset.go). Discovery chooses which
code to inspect. It has no inventory of the declarations affected by a change.

In the [experiment on tektoncd/pipelines-as-code#3023][experiment], three runs
with the same final image published 4, 6, and 5 of the 7 findings reported by
Copilot. The strongest run found all six substantive issues; none found the
YAML comment mismatch. The sort defect and cross-file label mismatch appeared
inconsistently. No false positives were identified among the published findings
in those runs. This is evidence from one deliberately broken PR, not an estimate
of general recall or false-positive rate.

Structural context could make investigation more consistent. An AST describes
syntax; it does not prove behavioral equivalence or resolve all dependencies.

## Proposed decision

Use Go's `go/parser`, `go/ast`, and `go/token` to build an inventory from the
existing immutable before/head snapshots. Map changed lines to enclosing
functions, methods, types, and package-level declarations on each side.
Include changes inside function literals under their enclosing declaration.
Keep imports, comments, and changes outside declarations visible as file-level
entries rather than dropping them.

Give discovery a bounded inventory automatically. Each entry should identify
the revision, path, declaration kind and name, source range, and related hunks.
Keep original source available through the existing tools. Do not dump complete
ASTs into the prompt.

Match declarations conservatively using file mappings, declaration names, and
receiver information. Represent additions and deletions explicitly. Renames,
moves, duplicate names, and ambiguous matches must remain visible; matching is
an aid to navigation, not proof that two declarations have the same identity.
Handle changes on the deleted side even when no corresponding declaration
exists at head.

Parse snapshot contents in memory without loading packages, downloading
dependencies, executing repository code, or reading the host workspace. Report
parse failures, excluded files, unsupported syntax, and inventory truncation.
Keep the text-based review path available for unsupported languages and
incomplete parses. Do not infer absence from a partial inventory.

The collected Git diff remains authoritative for publication anchors. Findings
still require source evidence and verification under the existing rules.

## Alternatives

[Difftastic](https://github.com/Wilfred/difftastic) compares syntax and reduces
formatting noise. Its primarily human-oriented output is useful when inspecting
refactors, but it does not supply a resolved reference index. Replacing Paco's
diff representation would also require preserving GitHub line mappings.

[GumTree](https://github.com/GumTreeDiff/gumtree) produces syntax-aligned edits
and detects moved or renamed elements. It is a candidate if declaration mapping
proves inadequate for refactors. Parser integration and tree matching add scope
that the initial inventory does not need.

More prompt instructions leave selection of context entirely to the model.
The current discovery prompt already asks it to investigate callers and
normalization consumers.

## Consequences

The Go standard library keeps the initial implementation small and compatible
with Paco's no-subprocess rule. It cannot parse syntax newer than the parser
bundled with Paco. Parsing files independently also does not select a valid
package build: build tags, platform variants, and excluded source can leave
multiple or incomplete views of a declaration.

An inventory may help the model inspect the full sort function, but it cannot
establish the sort contract violation itself. Connecting secret creation with
label selection may require following shared keys and values beyond direct
function references.

[ADR 0002](0002-discovery-coverage-and-follow-up.md) uses this inventory to
track investigation gaps. [ADR 0003](0003-type-resolved-go-navigation.md)
considers type-resolved navigation separately.

## Evaluation

Test range mapping for additions, deletions, methods, nested functions,
file renames, ambiguous matches, and partial parses. Confirm that unchanged
source evidence and Git diff coordinates survive inventory generation.
Exercise bounds and unsupported input without reporting complete coverage.

Compare baseline and inventory-assisted review on several immutable PR
revisions, including safe changes and cross-file defects. Hold model and
budgets fixed, repeat runs, and adjudicate findings manually. Measure recall,
false positives, context gaps, token usage, and latency. Use PR #3023 as a
regression example, not the sole acceptance corpus. Set rollout thresholds
before the experiment; this proposal does not claim a measured improvement.

## References

- [Go parser documentation](https://pkg.go.dev/go/parser)
- [Source snapshot boundaries](../../internal/source/snapshot.go)
- [Collected diff parsing](../../internal/diff/parse.go)
- [Current development rules](../development.md#rules)

[experiment]: https://github.com/tektoncd/pipelines-as-code/pull/3023#issuecomment-6064309391
