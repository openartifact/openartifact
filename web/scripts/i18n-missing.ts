// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// `bun run i18n:missing [--locale <tag>]`: prints, as JSON, the keys each locale
// still lacks with the English text to translate: { "<tag>": { "<key>": "<English>" } }.
// Translate the values and write them back with `bun run i18n:apply <file>`.

import { missingEntries } from '../src/i18n/catalogCheck'
import { SOURCE_LOCALE, SUPPORTED_LOCALES } from '../src/i18n/locales'
import { loadLocale } from './i18n-io'

const args = process.argv.slice(2)
const only = args.includes('--locale') ? args[args.indexOf('--locale') + 1] : undefined
const en = loadLocale(SOURCE_LOCALE)

const out: Record<string, Record<string, string>> = {}
for (const { tag } of SUPPORTED_LOCALES) {
  if (tag === SOURCE_LOCALE || (only && only !== tag)) continue
  const entries = missingEntries(tag, en, loadLocale(tag))
  if (entries.length > 0) out[tag] = Object.fromEntries(entries.map((e) => [e.key, e.source]))
}
console.log(JSON.stringify(out, null, 2))
