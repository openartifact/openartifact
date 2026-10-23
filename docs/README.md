<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# OpenArtifact documentation

Self-hosted artifact repository for 16 package formats. The [project README](../README.md) is the short overview; the details are here.

## Getting started

| Page | Contents |
|---|---|
| [Installation](getting-started/installation.md) | Docker Compose, Helm, release binaries and source builds; first login and password reset; installing the web UI as an app (PWA) |
| [Configuration](getting-started/configuration.md) | Load order, env-only variables, and every setting with env var, YAML key and default |
| [Upgrading](getting-started/upgrading.md) | Forward-only migrations, image tags, per-method upgrade steps, secrets to keep, rollback |

## Package formats

| Page | Contents |
|---|---|
| [Format matrix](formats/README.md) | Client URLs, hosted/proxy/group support, upload methods, authentication |
| [Maven](formats/maven.md) · [npm](formats/npm.md) · [PyPI](formats/pypi.md) · [Docker/OCI](formats/docker.md) | Client setup and publishing |
| [Helm](formats/helm.md) · [Debian](formats/debian.md) · [RPM](formats/rpm.md) · [Go](formats/go.md) | Client setup and publishing |
| [Cargo](formats/cargo.md) · [NuGet](formats/nuget.md) · [RubyGems](formats/rubygems.md) · [Conda](formats/conda.md) | Client setup and publishing |
| [Conan](formats/conan.md) · [Ivy and sbt](formats/ivy-sbt.md) · [Raw](formats/raw.md) | Client setup and publishing |

## Repositories

| Page | Contents |
|---|---|
| [Hosted, proxy and group](repositories/hosted-proxy-group.md) | Repository types, proxy cache and serve-stale, groups, the public flag |
| [Cleanup and quotas](repositories/cleanup-and-quotas.md) | Cleanup policies, per-repository and per-project storage quotas, project default cleanup policies |
| [Trash](operations/trash.md) | Deleted artifacts and repositories stay restorable for a retention period; immutable releases |
| [Properties](repositories/properties.md) | Artifact key/value properties: API, limits, search |
| [Release bundles](repositories/release-bundles.md) | Release bundles and promotion pipelines |
| [Migrating from Nexus / Artifactory](migration/nexus-artifactory.md) | `oa import`: flags, retries, what is preserved |

## Security

| Page | Contents |
|---|---|
| [Authentication](security/authentication.md) | Local login, sessions, CSRF, API tokens, legacy key, anonymous access, IP and rate limits |
| [Single sign-on](security/sso.md) | OIDC and SAML providers, provisioning, group mapping |
| [RBAC](security/rbac.md) | Users, groups, permission targets, request-to-action mapping, projects (repository ownership, member and group access) |
| [Projects](security/projects.md) | Project roles, member and group self-service, who may create projects, audit events, API |
| [Scanning](security/scanning.md) | Vulnerability, secret and license scans, quarantine |
| [SBOM and VEX](security/sbom.md) | CycloneDX / SPDX 2.3 and 3.0 SBOMs, OpenVEX / CycloneDX VEX, signed attestations (cosign), Trivy, Grype |
| [Dependency-Track](security/dependency-track.md) | Pushing the BOM on upload and scan and the VEX after analysis; project naming, permissions, per-repository overrides |
| [License metadata](security/licenses.md) | How licenses are read from package files: precedence per format, unresolved licenses, limits and limitations |
| [Signing](security/signing.md) | GPG key management and signing of APT/RPM metadata and release bundles; verifying release archives and images |

## Operations

| Page | Contents |
|---|---|
| [Storage](operations/storage.md) | Blob backends and their settings, content addressing, deduplication |
| [Storage tiers](operations/storage-tiering.md) | Named tiers on several backends, lifecycle policies, the crash-safe mover, promote-on-read, dry run |
| [Trash](operations/trash.md) | Soft delete: retention, restore, purge, immutable releases, repository delete confirmation |
| [Backup](operations/backup.md) | Backup and restore of records and blobs |
| [Monitoring](operations/monitoring.md) | Health, Prometheus metrics, audit log, logging |
| [Webhooks](operations/webhooks.md) | Events, HMAC signatures, retries, delivery log |
| [Performance](operations/performance.md) | Benchmarks, load tests, pprof, auth cache and other tuning |

## Integrations

| Page | Contents |
|---|---|
| [CLI](integrations/cli.md) | `oa` commands (including `trash` and repository delete confirmation), login and configuration |
| [MCP server](integrations/mcp.md) | Tools, permissions and client configuration (Claude, Cursor, VS Code, Codex, omp) |
| [REST API](integrations/api.md) | OpenAPI spec, Swagger UI, authentication, conventions |
| [AI analysis](integrations/ai-analysis.md) | Optional LLM analysis of package manifests |
| [yap](integrations/yap.md) | Build deb/rpm packages with yap, publish them and install from clients |

## Development

| Page | Contents |
|---|---|
| [Development guide](development/README.md) | Toolchain, build, test, lint, generate, CI and release pipeline |
| [Architecture](development/architecture.md) | Code layout, request flow, routing invariant, storage and database split |

Contributor rules: [CONTRIBUTING.md](../CONTRIBUTING.md) and [AGENTS.md](../AGENTS.md). Security reports: [SECURITY.md](../SECURITY.md).
