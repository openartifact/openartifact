<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Governance

OpenArtifact is maintained by The OpenArtifact Authors under the
[openartifact](https://github.com/openartifact) GitHub organization.

## Roles

- **Contributors** open issues and pull requests; see
  [CONTRIBUTING.md](CONTRIBUTING.md).
- **Maintainers** have write access, review and merge pull requests, triage
  issues, cut releases and handle private security reports
  ([SECURITY.md](SECURITY.md)). They are responsible for keeping CI green and
  releases verifiable.
- **Organization owners** manage repository settings, secrets, and maintainer
  membership.

## Decisions

Day-to-day decisions are made in issues and pull requests by consensus of the
maintainers; if consensus fails, the organization owners decide. Changes that
affect security, licensing or the public API are discussed in an issue first.
New maintainers are invited by the owners after sustained, high-quality
contributions.

## Continuity

The source, issues, release signing identity (GitHub OIDC keyless, tied to the
release workflow, not to a personal key) and the container registry all live in
the organization, and organization ownership is held by more than the
individual maintainer where possible, so the project can continue if one person
becomes unavailable. All build and release steps are scripted in the repository.

## Roadmap

Planned work is tracked in GitHub issues and milestones; shipped changes are in
the release notes.
