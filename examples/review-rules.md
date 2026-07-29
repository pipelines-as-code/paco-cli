# Review Rules

<!-- Place this file at .tekton/ai/REVIEW.md in your repository. -->
<!-- Paco loads these rules from the base branch (not the PR branch)  -->
<!-- to prevent a PR from weakening its own review criteria.          -->

## Priorities

- Focus on correctness, security, and error handling.
- Flag potential data races and concurrency issues.
- Check for proper input validation at system boundaries.

## Testing

- New features must include tests.
- Bug fixes should include a regression test.

## Style

- Follow the project's existing conventions.
- Keep functions short and focused.
