// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// `bun run i18n:check [--locale <tag>]`: validates web/src/i18n/locales.
// Exit 1 on errors: keys a locale has but English does not, placeholder sets
// that differ, plural forms the language does not use, English keys no code
// refers to, files that are not catalogs, and every key a locale lacks (list
// them with `bun run i18n:missing`, fill them with `bun run i18n:apply`).

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { AREAS } from '../src/i18n/areas'
import { SOURCE_LOCALE, SUPPORTED_LOCALES } from '../src/i18n/locales'
import { checkLocale, checkSource, findUnusedKeys, type Issue, type LocaleFiles } from '../src/i18n/catalogCheck'
import type { Messages } from '../src/i18n/translate'

const root = join(import.meta.dir, '..', 'src')
const localesDir = join(root, 'i18n', 'locales')

const args = process.argv.slice(2)
const only = args.includes('--locale') ? args[args.indexOf('--locale') + 1] : undefined

const errors: string[] = []

function load(tag: string): LocaleFiles {
  const dir = join(localesDir, tag)
  const files: Record<string, Messages> = {}
  if (!existsSync(dir)) return files
  for (const name of readdirSync(dir)) {
    const area = name.replace(/\.json$/, '')
    if (!name.endsWith('.json') || !(AREAS as readonly string[]).includes(area)) {
      errors.push(`${tag}/${name}: not a catalog file (known areas: ${AREAS.join(', ')}; add new ones to src/i18n/areas.ts)`)
      continue
    }
    try {
      const parsed: unknown = JSON.parse(readFileSync(join(dir, name), 'utf8'))
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('not a flat JSON object')
      files[area] = parsed as Messages
    } catch (err) {
      errors.push(`${tag}/${name}: ${(err as Error).message}`)
    }
  }

  return files
}

function sourceText(dir: string): string {
  let out = ''
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === 'locales' || name === '__tests__' || name === 'generated') continue
      out += sourceText(p)
    } else if (/\.(ts|vue)$/.test(name) && !/\.(test|spec)\.ts$/.test(name)) out += readFileSync(p, 'utf8') + '\n'
  }

  return out
}

const en = load(SOURCE_LOCALE)
for (const area of AREAS) if (!en[area]) errors.push(`en/${area}.json is missing`)

const issues: Issue[] = [...checkSource(en)]
for (const { area, key } of findUnusedKeys(en, sourceText(root))) {
  issues.push({ level: 'error', locale: 'en', area, key, message: 'key is not used by any source file' })
}

const stats: { tag: string; total: number; translated: number; missing: number; files: number; absent: string[] }[] = []
for (const { tag } of SUPPORTED_LOCALES) {
  if (tag === SOURCE_LOCALE || (only && only !== tag)) continue
  const files = load(tag)
  const result = checkLocale(tag, en, files)
  issues.push(...result.issues)
  const present = Object.keys(files).length
  if (present < AREAS.length) {
    issues.push({ level: 'warn', locale: tag, area: '-', key: '-', message: `${AREAS.length - present} catalog file(s) absent: ${AREAS.filter((a) => !files[a]).join(', ')}` })
  }
  stats.push({ tag, ...result.stats, files: present, absent: AREAS.filter((a) => !files[a]) })
}
// Directories under locales/ that are not a supported locale never load.
for (const dir of readdirSync(localesDir)) {
  if (!statSync(join(localesDir, dir)).isDirectory()) continue
  if (!SUPPORTED_LOCALES.some((l) => l.tag === dir)) errors.push(`locales/${dir}: not in SUPPORTED_LOCALES (src/i18n/locales.ts)`)
}

const enKeys = Object.values(en).reduce((n, m) => n + Object.keys(m).length, 0)
console.log(`English catalog: ${enKeys} keys in ${Object.keys(en).length} files`)
for (const s of stats) {
  const pct = s.total ? ((s.translated / s.total) * 100).toFixed(1) : '0.0'
  console.log(`  ${s.tag.padEnd(8)} ${String(s.translated).padStart(5)}/${s.total} translated (${pct}%), ${s.missing} missing, ${s.files} files`)
  if (s.absent.length > 0) console.log(`  ${''.padEnd(8)} missing catalogs: ${s.absent.join(', ')}`)
}

const real = issues.filter((i) => i.level === 'error')
const warnings = issues.filter((i) => i.level === 'warn')
const fmt = (i: Issue) => `  ${relative(root, join(localesDir, i.locale, `${i.area}.json`))}: ${i.key}: ${i.message}`
for (const w of warnings) console.log(fmt(w).replace(/^ {2}/, '  warn '))

for (const e of errors) console.error(`error: ${e}`)
for (const e of real) console.error(`error:${fmt(e)}`)
if (errors.length + real.length > 0) {
  console.error(`i18n check failed: ${errors.length + real.length} error(s)`)
  process.exit(1)
}
console.log('i18n check passed')
