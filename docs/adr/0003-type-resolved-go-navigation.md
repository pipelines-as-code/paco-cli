# ADR 0003: Type-resolved Go navigation

## Status

Proposed for investigation. Integrating gopls requires an architectural decision;
this record does not authorize subprocesses, dependency downloads, or broader
filesystem access.

## Context

Literal search and AST pattern matching can return unrelated identifiers with
the same spelling. They can also miss relationships through interfaces and
aliases. Definitions, references, and implementation queries could help Paco
follow changed behavior across Go packages.

The existing [development rules](../development.md#rules) prohibit subprocesses
and restrict repository tools to collected snapshots and diff data.
Snapshots are filtered and bounded, not complete build workspaces. Launching
gopls against an arbitrary checkout would violate that design.

## Proposed decision

Evaluate type-resolved navigation only after measuring the simpler
[change inventory](0001-go-aware-change-inventory.md). Target definition,
reference, and interface-implementation lookups tied to a revision and source
position. Results must include source locations and analysis limitations.

Compare gopls with an in-process analysis based on Go type information.
gopls provides navigation through LSP but needs workspace and toolchain
management. `go/types` can support in-process analysis, but requires a controlled
importer and package data. `go/packages` can invoke external tooling, so choosing
a library does not by itself satisfy the no-subprocess rule.

Before implementation, choose whether to retain entirely in-process analysis
or approve a separate, isolated indexing component. An external component needs
an explicit trust boundary: immutable revision inputs, constrained filesystem
access, resource limits, and validated outputs referring only to allowed source.
Dependency and toolchain provisioning must be deliberate. Do not automatically
download dependencies or execute repository-supplied tools to make a query work.

Keep before and head analyses separate. Identify the build configuration,
including platform and tags, used for each query. Report excluded packages,
unresolved imports, generated source gaps, unsupported toolchains, and failed
analysis as limitations. Text and structural searches may still help, but must
not be labeled type-resolved results.

## Alternatives

Stay with syntactic lookups and explicit ambiguity. This preserves the current
snapshot-only architecture and may provide enough context for most reviews.

Run gopls inside the current review process against a full checkout. This offers
familiar navigation, but expands access and execution capabilities and is not
compatible with current rules without a separate decision.

Use a prebuilt reference index. This could avoid analysis during review, but
the index must match the exact revisions and build configuration, and obey the
same source-exclusion rules. A stale index cannot support source claims.

## Consequences

Type information can disambiguate names and expose interface relationships.
It does not establish a complete runtime call graph or follow all data flow.
The label mismatch from the PR experiment may require connecting writers and
readers of the same key even when symbol references are accurate.

gopls reference results depend on the analyzed build configuration. A Linux
view is not evidence that no Windows-only caller exists. Filtered snapshots
and unavailable dependencies may prevent resolution entirely.

This proposal has greater operational and maintenance cost than parsing source
files. It should remain optional to the investigation until evaluation justifies
the architectural change.

## Evaluation

Use fixtures covering aliases, shadowed identifiers, interface methods,
cross-package callers, build tags, unresolved imports, and revision changes.
Check results against known locations and confirm incomplete analysis is
reported rather than converted into an empty successful lookup.

Evaluate isolation, source exclusions, output validation, cancellation, and
resource limits before a live integration. Query results must not expose
excluded dependency or workspace content.

Compare inventory-assisted review with and without resolved navigation on the
same PR revisions, model, and budget. Repeat and adjudicate findings manually;
measure cross-file recall, false positives, unresolved-query frequency, cost,
and latency. Require evidence that resolution adds value over cheaper searches
before choosing an integration and rollout policy.

## References

- [gopls navigation and build-configuration limitations](https://go.dev/gopls/features/navigation)
- [Go type checker](https://pkg.go.dev/go/types)
- [Go package loading](https://pkg.go.dev/golang.org/x/tools/go/packages)
- [Snapshot collection](../../internal/source/snapshot.go)
