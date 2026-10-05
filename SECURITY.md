# Security Policy

## Reporting a Vulnerability

Report vulnerabilities through GitHub's private vulnerability reporting:

<https://github.com/pipelines-as-code/paco-cli/security/advisories/new>

Do not open a public issue for security vulnerabilities.

## Scope

Security-sensitive areas include:

- Credential redaction patterns
- Secret scanning of model output
- Write-access filtering for existing feedback
- Base-branch-only rule loading
- The source snapshot and the model's read-only tools
- GitHub and model API clients (token scoping, redirects)
