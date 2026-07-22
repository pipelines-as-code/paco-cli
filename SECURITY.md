# Security Policy

## Reporting a Vulnerability

If you find a security vulnerability in paco-cli, please report it
responsibly through GitHub's private vulnerability reporting:

<https://github.com/pipelines-as-code/paco-cli/security/advisories/new>

Do not open a public issue for security vulnerabilities.

## Scope

paco-cli handles credential redaction, secret scanning, and trust
filtering for AI-generated code reviews. Security-sensitive areas include:

- Credential redaction patterns
- Secret scanning of model output
- Write-access filtering for existing feedback
- Base-branch-only rule loading (preventing PRs from weakening review rules)
- Subprocess execution and environment sanitization
