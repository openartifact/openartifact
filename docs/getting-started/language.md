<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Language

The web UI is available in English, Italian, German, French, Spanish, Brazilian Portuguese, Simplified Chinese and Japanese. Where a text is still missing, the UI shows the English one. Package clients, the API, the CLI and the server logs stay in English.

## Choosing the language

By default the UI follows your browser: the first language in the browser's preference list (**Settings > Languages** in most browsers) that has a translation is used. Portuguese in any variant uses Brazilian Portuguese, and `zh`, `zh-CN` and `zh-Hans` use Simplified Chinese. Traditional Chinese has no translation, so the next language on your list is tried; with nothing matching, the UI is in English.

To pick a language yourself:

- Signed in: open the user menu in the header and choose **Language**.
- Signed out: use the language field at the top right of the sign-in page.

Each language is listed by its own name, so you can find yours when the page is in a language you do not read. **Browser default** goes back to following the browser. The change applies at once, without reloading the page or losing what you typed in the sign-in form.

The choice is stored in this browser only (local storage, key `oa.locale`), not in your account and not on the server. Another browser or device starts from its own browser default, and clearing the site data forgets the choice.

## What follows the language

- All UI text, including dialogs and messages.
- Dates, times, relative times ("3h ago"), numbers and lists, written the way the language writes them.
- The sort order of names in tables and lists.
- The page's `lang` attribute, so screen readers and the browser's spell checker use the right language.

The "Powered by OpenArtifact" attribution is the same in every language (see [White-label branding](../operations/branding.md)). Product, package-format and tool names, commands, file names and API fields are never translated.

## Fixing a translation

Translations are JSON files in the repository, corrected with a pull request; see [Translating the web UI](../development/translations.md).
