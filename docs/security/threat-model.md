<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Threat model and secure design

## Assets and trust boundaries

| Asset | Where | Boundary |
|---|---|---|
| Artifacts (packages, images) | object storage, metadata in PostgreSQL | clients reach them only through the HTTP API |
| Credentials (passwords, API tokens, SSO sessions) | PostgreSQL | hashed (argon2id) or sealed with AES-256-GCM under `OPENARTIFACT_SECRET_KEY` |
| Signing keys (GPG) | PostgreSQL, sealed | never returned by the API |
| Upstream proxies | outbound HTTP(S) | untrusted content; URLs validated, responses size-limited |

Trust boundaries: internet to the server (TLS terminated by a reverse proxy or
the server), server to PostgreSQL and object storage (trusted network), server
to upstream registries and webhooks (untrusted).

## Threats and mitigations

| Threat | Mitigation |
|---|---|
| Unauthorized read/write of packages | authentication on every write path; RBAC per repository and project ([rbac](rbac.md), [projects](projects.md)) |
| Credential theft from the database | argon2id password hashes, hashed API tokens, sealed secrets |
| Path traversal and malicious archive names | paths and archive entries validated before storage |
| SSRF through proxy repositories and webhooks | destination validation, redirect and size limits |
| Malicious or vulnerable packages | vulnerability scanning, license policy, quarantine ([scanning](scanning.md)) |
| Tampered releases | checksums, keyless cosign signatures, build provenance ([signing](signing.md)) |
| Vulnerable dependencies | Dependabot, `govulncheck`, OSV and Trivy in CI and a scheduled scan |
| Compromised CI | pinned action SHAs, minimal workflow permissions, zizmor and CodeQL |

## Secure design principles applied

Least privilege (minimal workflow and role permissions), fail closed on
authorization errors, input validation at the HTTP boundary, defense in depth
(RBAC plus audit log), economy of mechanism (a single binary with PostgreSQL
and S3 storage), secure defaults, and reuse of vetted libraries instead of
custom cryptography. Cryptography uses the Go standard library and
`golang.org/x/crypto` (argon2); passwords are never stored reversibly, TLS
verification is on by default, and random values come from `crypto/rand`.

## Assurance argument

The security requirements above are verified by the automated test suite
(including authorization tests per format), static analysis (`golangci-lint`,
CodeQL), dependency and image scanning on every pull request,
and the process in [SECURITY.md](../../SECURITY.md) for reports.
