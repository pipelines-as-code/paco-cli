# ADR 0004: Multi-language structural search

## Status

Proposed. Parser selection, supported languages, and packaging require an
implementation decision. No new tools or dependencies are approved here.

## Context

Paco's literal search works across text files but cannot distinguish a call
expression from a comment containing the same text. Structural patterns can
find related code despite differences in spacing or argument names.

[ADR 0001](0001-go-aware-change-inventory.md) proposes a Go-only starting point.
Reviewing other languages may justify a broader syntax-aware search facility.
This is separate from the type resolution considered in
[ADR 0003](0003-type-resolved-go-navigation.md).

## Proposed decision

Evaluate ast-grep or embedded Tree-sitter parsers for bounded, read-only
structural queries over collected before/head snapshots. Return the revision,
path, matched source range, and original source text. Mark results as syntactic
matches, not resolved references to a particular declaration.

Limit the initial language set to demonstrated review needs. Pin parser
versions and document supported syntax. Report parse errors, unsupported
languages, unavailable files, cancelled queries, and truncation. An incomplete
parse or result set must not support a claim that matching code is absent.

Restrict queries to search. Do not expose rewriting, shell commands, arbitrary
file paths, repository-provided configuration, or parser installation through
model tool arguments. Apply the existing snapshot exclusions and output bounds.
Bound pattern complexity, parsing work, and match volume as well as response
size.

Compare the ast-grep pattern interface with direct Tree-sitter queries before
selecting an implementation. ast-grep offers code-shaped patterns;
Tree-sitter supplies parsing and tree queries but does not provide a complete
review-oriented search tool by itself.

The ast-grep CLI conflicts with Paco's no-subprocess rule. An implementation
must either use a suitable embedded integration or obtain approval for a
separate constrained component. Embedding also requires checking parser
licenses, native dependencies, release packaging, and supported architectures.

## Alternatives

Keep literal search for non-Go files. It has minimal maintenance cost and
clear limitations; representative PRs may show that it is sufficient.

Write a parser-specific adapter for each language using its native libraries.
This can avoid a common native parsing dependency, but multiplies query and
maintenance work.

Adopt language servers for each language. They can provide stronger symbol
resolution, but require language-specific workspace and dependency management.
Structural search is a smaller capability and should be evaluated independently.

## Consequences

Structural patterns can reduce irrelevant search results and find repeated
code shapes. They cannot establish that two calls target the same function,
or that two values share a producer and consumer. The model still needs to
inspect source and gather counterevidence.

Language grammars and node shapes differ. A generic query interface must not
promise identical behavior across parsers. Error recovery may return useful
partial trees, but the tool must expose that uncertainty.

Keeping literal search available avoids blocking unsupported languages.
Any fallback must identify its search mode and limitations rather than
silently presenting textual matches as structural results.

## Evaluation

Test patterns against comments, strings, formatting changes, nested expressions,
invalid syntax, parser-version gaps, and oversized inputs. Confirm exact source
range mapping for both revisions and preservation of snapshot exclusions.
Test cancellation and deterministic truncation without adding write access.

Choose a small multi-language PR corpus with known search-dependent defects
and safe changes. Compare literal-only and structural-assisted review with the
same model, revisions, and budgets, using repeated runs and human adjudication.
Measure useful matches, missed matches, recall, false positives, cost, latency,
and parser failures. Adopt parsers only where the measured benefit justifies
their packaging and maintenance cost.

## References

- [ast-grep](https://github.com/ast-grep/ast-grep)
- [Tree-sitter](https://tree-sitter.github.io/tree-sitter/)
- [Current source tools](../../internal/source/toolset.go)
- [Development rules](../development.md#rules)
