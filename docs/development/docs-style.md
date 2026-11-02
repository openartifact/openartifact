<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Documentation style

Read this before you write or change a page in `docs/`. It keeps the pages readable for people who have never seen the code, and it is the checklist a reviewer uses.

## Mechanics

- Every new `.md` file starts with the SPDX HTML comment header. Copy it from any existing page.
- Links are relative (`../security/authentication.md#api-tokens`). Check that the target file and the heading anchor exist. Anchors are GitHub-style slugs of the heading text.
- Outside code spans and code blocks, do not write `{{ }}` or bare tags such as `<token>`. The site renders the pages as Vue-flavoured markdown and fails the build on them. Write `<TOKEN>` inside backticks.
- `docs/reference/` is generated. Change the source (config struct, metric, CLI help, `internal/api/openapi.json`), run `make docs-gen` and commit the result. Never edit it by hand.
- A new feature, format, config key, CLI command or API area gets or updates a page in the same change, and the page is listed in the [documentation index](../README.md).
- Run `make docs-check` before you push. It fails on a stale generated page, a relative link whose file does not exist, a `#anchor` that matches no heading, and a page under `docs/` (outside `docs/reference/`) that the index does not link.

## Writing rules

1. **Open with the point.** The first one or two sentences say what the page lets you do and when to use it.
2. **Task first.** Put the common how-to, with copy-pasteable commands, before reference detail and edge cases. Put troubleshooting last. The first command appears within 15 lines of the title. Use tokens in examples, never passwords.
3. **One claim per sentence.** A bullet is at most about 40 words. Do not stack parenthetical caveats: state the rule, then the exception, and put the example in a code block.
4. **Tables are for lookup data.** A cell holds a word, a value or one short sentence. Move paragraphs below the table.
5. **No internals in user docs.** Do not mention table names, package paths, locks, queues, migration numbers or library names unless the reader needs them to act. If they do, say it in plain words. The pages under `docs/development/` and `docs/design/` may name internals.
6. **No release history in reference pages.** Notes such as "before 0.x" belong in [upgrading](../getting-started/upgrading.md). A reference page describes the current behaviour only.
7. **Voice.** Use "you" for steps. Use "the server" or "OpenArtifact" for behaviour. Write in the present tense.
8. **Descriptive headings.** A heading says what the section helps with (`Reset the admin password`), not what it is (`Overview`). Headings are unique within a page.
9. **One owner per fact.** State a fact on one page and link to it from the others. Generated pages own config keys, metrics, CLI flags and API endpoints.
10. **Keep facts correct.** Check any claim you add or change against the code on `main`. Run the commands you document.

## Names

- The server binary is `openartifact`, and in the container image `/app/openartifact`. The CLI is `oa`.
- The UI area for instance settings is **Settings**.
- Say "package format" or "format", "project" (not "organization"), and "access token".

## Before and after

Before:

> The service passes only the variables listed in its `environment:` block (BASE_URL, which is optional, and others), unless an override file is used, in which case see the section below, which also covers volumes.

After:

> The compose file passes only the variables it lists. To pass another one, add it to the `environment:` block of the `openartifact` service or to a compose override file.
