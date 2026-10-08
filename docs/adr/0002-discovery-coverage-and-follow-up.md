# ADR 0002: Discovery coverage and a targeted follow-up pass

## Status

Proposed. Pass policy, limits, and rollout criteria require an implementation
decision before this changes review behavior.

## Context

Increasing the available tool budget does not ensure that discovery uses it
to inspect relevant dependencies. The experiment described in
[ADR 0001](0001-go-aware-change-inventory.md) showed inconsistent discovery of
the sort and label defects even after raising the budget.

Paco logs candidate dispositions and incomplete tool results, but those records
do not establish which changed declarations received enough investigation.
Reading a function is observable; reasoning correctly about it is not.

## Proposed decision

Use the change inventory from ADR 0001 to track context gathered during
discovery. Record which declaration ranges were actually returned by tools,
partial reads, attempted dependency lookups, and unresolved context gaps.
Keep model-reported investigation conclusions separate from tool observations.
Neither should become a claim that a declaration is correct.

Offer one bounded follow-up discovery pass targeted at the remaining gaps.
Give it the relevant inventory entries, gathered context, existing candidate
identifiers, and incomplete lookups. Ask it to investigate those gaps rather
than repeat the whole review. A gap is a request for more context, not a
defect and not an obligation to produce a finding.

Charge both discovery passes to the same review budget. Reserve capacity for
verification; do not silently extend tool, token, or time limits. Before
implementation, choose the reservation policy and conditions for running or
skipping the pass. Report skipped or unfinished investigation explicitly.
Do not start an unbounded loop that tries to eliminate all gaps.

Deduplicate candidates from both passes before verification, preserving the
existing prior-feedback rules. Verify newly discovered candidates independently
of discovery claims, using original source evidence. Publication requirements
remain unchanged.

## Alternatives

Always running a second full review might improve recall, but repeats work
and spends more tokens even where the first pass gathered sufficient context.

A larger budget alone permits deeper investigation without directing it.
A stronger model may investigate more reliably, but requires its own controlled
comparison and does not supply an observable account of context gaps.

Adding more tools without automatically supplying an inventory still leaves
the model responsible for deciding when those tools are needed.

## Consequences

The tracker can expose missed context and guide another pass. It cannot measure
reasoning completeness. A tool response may contain a function without the model
examining the relevant branch.

Dependencies are open-ended, especially where code shares serialized keys or
communicates through external systems. Inventory completion must not become a
percentage advertised as review correctness. Prioritize bounded, observable
gaps and retain explicit uncertainty.

Passing earlier candidates to the follow-up model may reinforce its initial
assumptions. Keep candidate text untrusted and allow counterevidence to reach
verification. The extra pass may increase false candidates as well as recall.

## Evaluation

Test partial reads, truncated searches, missing snapshots, budget exhaustion,
duplicate candidates, and follow-up failures. Confirm that skipped work remains
visible and no extra pass bypasses verification or exceeds shared limits.

First compare inventory-only review with inventory plus follow-up, holding the
model, revisions, and total budgets fixed. Repeat across several PRs with
human-adjudicated findings. Measure substantive recall, false positives,
duplicate rate, cost, latency, and context gaps. A later experiment may compare
larger budgets, but should report that cost change separately.

Define minimum benefit and acceptable overhead before rollout. Do not infer
success merely from more tool calls or fewer inventory entries left unread.

## References

- [Verified review orchestration](../../internal/review/verified.go)
- [Shared model budget](../../internal/model/budget.go)
- [Discovery instructions](../../internal/review/prompts/discover.txt)
- [Evaluation guidance](../development.md#review-evaluations)
