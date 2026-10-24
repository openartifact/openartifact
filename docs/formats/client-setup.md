<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Client setup

Configure and publish commands for every client, for your own server URL and repository name. The snippets come from `web/src/lib/installCommands.ts`, the module behind the web UI's connect panel for a repository, so this page and the UI show the same commands. Credentials are always the placeholder `<TOKEN>`: create an API token as described in [authentication](../security/authentication.md).

<ClientSnippets />

On GitHub this page shows no snippets (they are rendered by the documentation site, <https://openartifact.github.io/>). Open a repository in the web UI to get the same commands, or read the per-format pages: [format matrix](README.md).
