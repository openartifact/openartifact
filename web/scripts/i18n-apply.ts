// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// `bun run i18n:apply <file.json>`: merges translations ({ "<tag>": { "<key>": "<text>" } },
// the shape `bun run i18n:missing` prints) into web/src/i18n/locales/<tag>/<area>.json,
// ordered like the English catalog. Nothing is written when any entry is invalid.

import { mkdirSync } from 'node:fs'
import { mergeCatalog, splitPlural } from '../src/i18n/catalogCheck'
import { AREAS } from '../src/i18n/areas'
import { SOURCE_LOCALE, SUPPORTED_LOCALES } from '../src/i18n/locales'
import { loadLocale, localesDir, writeCatalog } from './i18n-io'

const file = process.argv[2]
if (!file) {
  console.error('usage: bun run i18n:apply <file.json>')
  process.exit(2)
}
const input = JSON.parse(await Bun.file(file).text()) as Record<string, Record<string, string>>
const en = loadLocale(SOURCE_LOCALE)
const areaOf = (key: string): string | undefined => {
  const base = splitPlural(key)?.base
  return AREAS.find((a) => key in (en[a] ?? {}) || (base !== undefined && `${base}_other` in (en[a] ?? {})))
}

const errors: string[] = []
const writes: { tag: string; area: string; messages: Record<string, string> }[] = []
for (const [tag, entries] of Object.entries(input)) {
  if (tag === SOURCE_LOCALE || !SUPPORTED_LOCALES.some((l) => l.tag === tag)) {
    errors.push(`${tag}: not a translated locale`)
    continue
  }
  const own = loadLocale(tag)
  const byArea: Record<string, Record<string, string>> = {}
  for (const [key, value] of Object.entries(entries)) {
    const area = areaOf(key)
    if (!area) errors.push(`${tag}: ${key}: not in the English catalog`)
    else (byArea[area] ??= {})[key] = value
  }
  for (const [area, add] of Object.entries(byArea)) {
    try {
      writes.push({ tag, area, messages: mergeCatalog(en[area]!, own[area] ?? {}, add) })
    } catch (err) {
      errors.push(`${tag}/${area}: ${(err as Error).message}`)
    }
  }
}
if (errors.length > 0) {
  for (const e of errors) console.error(`error: ${e}`)
  process.exit(1)
}
for (const w of writes) {
  mkdirSync(`${localesDir}/${w.tag}`, { recursive: true })
  writeCatalog(w.tag, w.area, w.messages)
}
console.log(`applied ${writes.length} catalog file(s)`)
