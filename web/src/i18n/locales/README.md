<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# UI catalogs

One directory per language (`en` is the source, the others are translations), one JSON file per area
(`admin.json`, `repos.json`, ...). Keys are flat and dotted; the same key in the same file exists in every
language. Plural messages are `<key>_one` / `<key>_other` (and the other CLDR forms a language has).

Corrections are welcome as pull requests.

How to change or add a string, add a language and run the checks: [Translating the web UI](../../../../docs/development/translations.md).
