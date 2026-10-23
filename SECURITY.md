<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities **privately** by email to
**morf3089@gmail.com**. Do not open public issues or pull requests for
security problems.

Include, where possible:

- a description of the issue and its impact,
- the affected version (`VERSION` file or `/api/v1/system/info`) and configuration,
- steps or a proof of concept to reproduce.

You will receive an acknowledgement within **3 days** and an initial
assessment within **14 days**. We then coordinate a fix and disclosure
timeline with you. Please give us reasonable time to release a fix before any
public disclosure. Reporters are credited in the release notes and the
published GitHub security advisory unless they ask to stay anonymous.

## How vulnerabilities are handled

1. The report is triaged privately and, if confirmed, tracked in a private
   GitHub security advisory.
2. Medium or higher severity issues are fixed within **60 days** of
   confirmation, and critical ones as fast as possible (target: 14 days).
3. The fix ships in a new release whose notes name the advisory (CVE/GHSA id)
   for every publicly known vulnerability it fixes.
4. Dependencies are monitored by Dependabot, `govulncheck`, Trivy and OSV in
   CI and the scheduled scan; fixable HIGH/CRITICAL findings block releases.

Security design assumptions and trust boundaries are described in the
[threat model](docs/security/threat-model.md). Release artifacts can be
verified as described in [signing](docs/security/signing.md).

## Supported versions

Security fixes are applied to the latest release only.
