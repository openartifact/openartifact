<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Trademark policy

> **DRAFT for maintainer review.** This policy was drafted together with the white-label branding feature. It is not legal advice and has not been reviewed by a lawyer. Until a maintainer approves it, treat it as a proposal.

"OpenArtifact", the OpenArtifact logo and the OpenArtifact mark (together, the **marks**) identify the software released by The OpenArtifact Authors. The source code is licensed under the [GNU Affero General Public License v3.0](LICENSES/AGPL-3.0-or-later.txt). That license grants you the right to use, modify and redistribute the code. It does **not** grant any right to use the marks, except what applicable law allows (see [Fair use](#fair-use)).

The goal of this policy is that people can tell whether the software they run is the real thing, not to stop anyone from using, forking or selling services around OpenArtifact.

## Unmodified builds

You may use the name OpenArtifact, and the logo, to refer to a build of OpenArtifact that you did **not** modify: the official release binaries and container images, or a build from an unmodified release tag. This includes running it internally, hosting it for others, and packaging it for a distribution, as long as you do not suggest that you are the OpenArtifact project or that the project endorses you.

Changing only the settings the software provides for it (configuration, and the [branding feature](docs/operations/branding.md)) does not count as modifying the build.

## Modified builds

If you distribute a build whose source or behaviour you changed, or offer such a build to others as a service, you must **not** use the marks as the name of your product or service, in its logo, domain name, package or image name, or in a way that suggests it is the official software.

You may say that your product is **"based on OpenArtifact"** or "a fork of OpenArtifact", in plain text, as long as the statement is true and it is clear that your product is a separate one. AGPL-3.0 section 13 still applies to you: offer the users of your modified service the corresponding source (the "Source code" link in the UI points to `branding.sourceUrl` for this reason).

Use the [branding feature](docs/operations/branding.md) to give your build its own product name, logo, favicon and colours. It exists for this purpose. The "Powered by OpenArtifact" attribution and the source-code link in the UI are part of the software and must stay.

## Fair use

You may use the name without asking where the law lets you, for example to accurately refer to the project in documentation, articles, talks, comparisons, package metadata, or to state that your tool works with OpenArtifact ("compatible with OpenArtifact"). Do not use the logo for this unless you are allowed under the previous sections.

## Brand assets

The logo, mark and other files in the [`openartifact/brand`](https://github.com/openartifact/brand) repository, and the copies shipped in this repository's web UI, are licensed under [CC-BY-SA-4.0](LICENSES/CC-BY-SA-4.0.txt). That license covers copyright in the files: you may copy and adapt the files under its terms. It grants **no trademark rights**. Using a file under CC-BY-SA-4.0 does not entitle you to present your product under the marks, and an adapted logo that is confusingly similar to the marks falls under this policy.

## Contact

To ask for permission, report a misuse or ask a question about this policy, write to **morf3089@gmail.com** or open an issue at <https://github.com/openartifact/openartifact/issues> (do not post private details there).

## Changes

The maintainers may revise this policy. Changes are tracked in the git history of this file.
