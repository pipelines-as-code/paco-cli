# Contributing

## Getting Started

1. Fork and clone the repository
2. Install [pre-commit](https://pre-commit.com/) and run `pre-commit install`
3. Make your changes on a feature branch
4. Run `make check` before submitting a pull request

## Code Style

- Format Go code with `make fumpt`
- Use table-driven tests with `gotest.tools/v3/assert`
- Follow [Conventional Commits](https://www.conventionalcommits.org/) for
  commit messages

## Pull Requests

- Link to the relevant issue
- Include tests for new functionality
- Ensure `make check` passes

## AI Assistance

If you use AI tools to help write code, you are responsible for
understanding, reviewing, and being able to explain every line you submit.

## Code of Conduct

This project follows the [Contributor Covenant](code-of-conduct.md).
