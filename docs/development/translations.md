<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Translating the web UI

The web UI is translated into Italian, German, French, Spanish, Brazilian Portuguese, Simplified Chinese and Japanese; English is the source. The non-English catalogs have not been reviewed by native speakers yet; corrections are welcome as pull requests. This page is for translators and for contributors who add or change UI text.

For what the language setting does for users, see [Language](../getting-started/language.md).

## Where the texts live

```text
web/src/i18n/locales/en/*.json        source of truth, edited in code reviews
web/src/i18n/locales/<tag>/*.json     translations, edited by pull request (it, de, fr, es, pt-BR, zh-Hans, ja)
web/src/i18n/glossary/<tag>.csv       preferred terms per language, with the style guide in README.md
web/src/i18n/areas.ts                 the list of catalog files
web/scripts/i18n-check.ts             the check CI runs
```

A catalog file is a flat JSON object of dotted keys to strings, split by area (`admin.json`, `repos.json`, ...). A message with a number has one entry per plural form, `<key>_one` and `<key>_other` (and `_zero`, `_two`, `_few`, `_many` for languages that distinguish them, in the [CLDR](https://cldr.unicode.org/) sense). Japanese and Simplified Chinese have only `_other`. Placeholders are `{name}`, filled by the code at runtime; keep them exactly as in the English text.

Never translate product names (OpenArtifact), package formats and tools (npm, Maven, Docker, ...), commands and flags, file names, paths, URLs or API field names. The "Powered by OpenArtifact" attribution is not in the catalogs at all.

## Adding or changing UI text

1. Write the English text in the right `locales/en/<area>.json`, with a key named after where it appears (`repos.new.nameHint`). Use it in code only through `t(key)`, `tp(key, { name })` for placeholders and `tpn(key, count)` for plurals, from `web/src/i18n`. A string built from translated pieces does not translate well; give the whole sentence one key with placeholders instead.
2. Leave the other languages alone: a key a language lacks shows the English text until someone translates it. (If you can write the translation, add it to the language files in the same pull request.)
3. Changing the meaning of an English text? Give it a new key and delete the old one, so translators are not left with an outdated translation under the same key. A pure typo fix keeps the key.
4. Run `cd web && bun run i18n:check`.

`bun run i18n:check` (also run by `bun run build` and by the `web` job in CI) fails on:

- a key in a language that is not in English, or that lives in another file than in English;
- a different set of `{placeholders}` than the English text (for a plural form, than the English form of the same kind);
- a plural form the language does not use (for example `_one` in Japanese), or a plural group without `_other`;
- an English key that no source file refers to (dynamic keys such as `` `status.${x}` `` keep every key under that prefix alive);
- invalid JSON, values that are not strings, and files that are not listed in `areas.ts`.

Missing translations are only reported (`bun run i18n:check --missing` lists them), because the UI falls back to English and a new English key must not block a release. A value that is an empty string counts as missing and shows the English text.

## Translators

To correct or improve a translation, edit the value in `web/src/i18n/locales/<tag>/<area>.json` and open a pull request; only the values change, never the keys or the `{placeholders}`. Rules for every language are in `web/src/i18n/glossary/README.md` (tone, terms that stay in English, plural forms), and each language's `.csv` there lists the preferred term for each recurring concept; use it, and inflect it instead of choosing a synonym. Run `cd web && bun run i18n:check` before you push: it catches a broken placeholder or plural form, which CI would reject too.

Check how a text looks in the UI before you finish a long label: texts of the same meaning are often longer in German and French and shorter in Japanese and Chinese, and table headers and buttons are narrow. Prefer the short common word.

## Adding a language

1. Add it to `SUPPORTED_LOCALES` (tag and the language's own name) and to `BY_LANGUAGE` (browser language to tag) in `web/src/i18n/locales.ts`, and a loader line in `LOADERS` in `web/src/i18n/index.ts`.
2. Add `web/src/i18n/glossary/<tag>.csv` and its row in the style guide.
3. Copy `web/src/i18n/locales/en` to `web/src/i18n/locales/<tag>` and translate the values; a value left empty is reported as missing and shows the English text. A language may start with only some of the area files.
4. Update the list of tags in `web/src/i18n/__tests__/locales.test.ts`, and `web/src/components/__tests__/LanguageSelect.test.ts`.

The build puts each language in its own chunk (`locale-<tag>-<hash>.js`), downloaded only when a user of that language opens the UI or switches to it. The chunks are not part of the initial bundle and not in the service worker's precache.
