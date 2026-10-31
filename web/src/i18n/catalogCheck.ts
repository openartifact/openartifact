// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The rules `bun run i18n:check` enforces on the catalogs (scripts/i18n-check.ts
// reads the files and calls these). Pure: no file or DOM access.

import { pluralCategories, type Messages } from './translate'

/** catalog files of one locale: area name → messages */
export type LocaleFiles = Readonly<Record<string, Messages>>

export interface Issue {
  level: 'error' | 'warn'
  locale: string
  /** area (file) the key lives in */
  area: string
  key: string
  message: string
}

export interface LocaleStats {
  /** keys the locale must have (see `requiredKeys`) */
  total: number
  /** required keys this locale has a non-empty value for (see `requiredKeys`) */
  translated: number
  missing: number
}

export interface CheckResult {
  issues: Issue[]
  stats: Record<string, LocaleStats>
}

const CATEGORIES = new Set(['zero', 'one', 'two', 'few', 'many', 'other'])
const PLURAL_SUFFIX = /^(.+)_(zero|one|two|few|many|other)$/

/** `foo_one` → { base: 'foo', category: 'one' }; any other key → null. */
export function splitPlural(key: string): { base: string; category: string } | null {
  const m = PLURAL_SUFFIX.exec(key)

  return m ? { base: m[1]!, category: m[2]! } : null
}

/** Sorted unique `{name}` placeholders of a message. */
export function placeholdersOf(message: string): string[] {
  return [...new Set([...message.matchAll(/\{(\w+)\}/g)].map((m) => m[1]!))].sort()
}

const same = (a: string[], b: string[]): boolean => a.length === b.length && a.every((v, i) => v === b[i])

function flatten(files: LocaleFiles): Map<string, { area: string; value: string }> {
  const out = new Map<string, { area: string; value: string }>()
  for (const [area, messages] of Object.entries(files)) {
    for (const [key, value] of Object.entries(messages)) out.set(key, { area, value })
  }

  return out
}

/** English plural groups: base key → categories present. */
function pluralGroups(keys: Iterable<string>): Map<string, Set<string>> {
  const groups = new Map<string, Set<string>>()
  for (const key of keys) {
    const p = splitPlural(key)
    if (!p) continue
    const set = groups.get(p.base) ?? new Set<string>()
    set.add(p.category)
    groups.set(p.base, set)
  }

  return groups
}

/** Problems of the English catalog itself: values that are not text, plural groups without `_other` or `_one`. */
export function checkSource(en: LocaleFiles): Issue[] {
  const issues: Issue[] = []
  const flat = flatten(en)
  for (const [key, { area, value }] of flat) {
    if (typeof value !== 'string' || value === '') issues.push({ level: 'error', locale: 'en', area, key, message: 'value is not a non-empty string' })
  }
  const english = pluralCategories('en')
  for (const [base, cats] of pluralGroups(flat.keys())) {
    const area = flat.get(`${base}_${[...cats][0]}`)!.area
    for (const cat of cats) {
      if (!english.includes(cat)) issues.push({ level: 'error', locale: 'en', area, key: `${base}_${cat}`, message: `plural form "${cat}" does not exist in English` })
    }
    for (const need of english) {
      if (!cats.has(need)) issues.push({ level: 'error', locale: 'en', area, key: `${base}_${need}`, message: 'plural group is missing this form' })
    }
  }

  return issues
}

/**
 * Keys a locale must translate: every plain English key, and of a plural group
 * the forms English has that the language also uses (`other` always).
 */
function requiredKeys(tag: string, english: Map<string, { area: string; value: string }>): string[] {
  const allowed = new Set(pluralCategories(tag))
  const groups = pluralGroups(english.keys())
  const out: string[] = []
  for (const key of english.keys()) {
    const plural = splitPlural(key)
    if (plural && groups.has(plural.base) && plural.category !== 'other' && !allowed.has(plural.category)) continue
    out.push(key)
  }

  return out
}

export interface MissingEntry {
  area: string
  key: string
  /** English text to translate (the `_other` form for a plural form English lacks) */
  source: string
}

/** Required keys the locale has no non-empty value for, in English catalog order. */
export function missingEntries(tag: string, en: LocaleFiles, files: LocaleFiles): MissingEntry[] {
  const english = flatten(en)
  const own = flatten(files)
  const out: MissingEntry[] = []
  for (const key of requiredKeys(tag, english)) {
    if ((own.get(key)?.value ?? '') !== '') continue
    const src = english.get(key)!
    out.push({ area: src.area, key, source: src.value })
  }

  return out
}

/**
 * Catalog `existing` with `add` merged in, ordered like English (`source`);
 * keys English lacks (plural forms of other languages) follow their `_other`.
 * Throws on a key that is not in English, an empty value, or placeholders that
 * differ from English.
 */
export function mergeCatalog(source: Messages, existing: Messages, add: Messages): Messages {
  const merged: Record<string, string> = { ...existing }
  for (const [key, value] of Object.entries(add)) {
    const plural = splitPlural(key)
    const ref = source[key] ?? (plural ? source[`${plural.base}_other`] : undefined)
    if (ref === undefined) throw new Error(`${key}: not in the English catalog`)
    if (value === '') throw new Error(`${key}: empty translation`)
    if (!same(placeholdersOf(ref), placeholdersOf(value))) {
      throw new Error(`${key}: placeholders {${placeholdersOf(value).join('} {')}} differ from English {${placeholdersOf(ref).join('} {')}}`)
    }
    merged[key] = value
  }
  const out: Record<string, string> = {}
  for (const key of Object.keys(source)) {
    if (key in merged) out[key] = merged[key]!
    const plural = splitPlural(key)
    if (plural?.category === 'other') {
      for (const k of Object.keys(merged)) {
        if (!(k in source) && splitPlural(k)?.base === plural.base) out[k] = merged[k]!
      }
    }
  }
  for (const key of Object.keys(merged)) if (!(key in out)) out[key] = merged[key]!

  return out
}

/**
 * Checks one translated locale against English. Errors: a key English does
 * not have (or lives in another file), a placeholder set that differs, a plural
 * form the language does not use, a plural group without `_other`, and every
 * missing key (the UI would fall back to English, but a string ships in every
 * language); empty values count as missing.
 */
export function checkLocale(tag: string, en: LocaleFiles, files: LocaleFiles): { issues: Issue[]; stats: LocaleStats } {
  const issues: Issue[] = []
  const english = flatten(en)
  const own = flatten(files)
  const groups = pluralGroups(english.keys())
  const allowed = new Set(pluralCategories(tag))

  for (const [key, { area, value }] of own) {
    if (typeof value !== 'string') {
      issues.push({ level: 'error', locale: tag, area, key, message: 'value is not a string' })
      continue
    }
    const plural = splitPlural(key)
    const isGroupForm = plural !== null && groups.has(plural.base)
    const src = english.get(key)
    if (!src && !isGroupForm) {
      issues.push({ level: 'error', locale: tag, area, key, message: 'key is not in the English catalog' })
      continue
    }
    const home = src ?? english.get(`${plural!.base}_other`)!
    if (home.area !== area) issues.push({ level: 'error', locale: tag, area, key, message: `key lives in ${home.area}.json in English` })
    if (value === '') continue
    if (isGroupForm && !allowed.has(plural.category)) {
      issues.push({ level: 'error', locale: tag, area, key, message: `plural form "${plural.category}" is not used by ${tag} (CLDR: ${[...allowed].join(', ')})` })
    }
    // A form English has is compared with that form; any other form (few, many…) with English "other".
    const reference = src ?? english.get(`${plural!.base}_other`)!
    const want = placeholdersOf(reference.value)
    const got = placeholdersOf(value)
    if (!same(want, got)) {
      issues.push({ level: 'error', locale: tag, area, key, message: `placeholders {${got.join('} {')}} differ from English {${want.join('} {')}}` })
    }
  }

  for (const base of groups.keys()) {
    const hasAny = [...allowed, 'one', 'other'].some((c) => (own.get(`${base}_${c}`)?.value ?? '') !== '')
    if (hasAny && (own.get(`${base}_other`)?.value ?? '') === '') {
      const area = english.get(`${base}_other`)!.area
      issues.push({ level: 'error', locale: tag, area, key: `${base}_other`, message: 'plural group has no "other" form' })
    }
  }

  let translated = 0
  let missing = 0
  const absent = new Set(missingEntries(tag, en, files).map((m) => m.key))
  for (const key of requiredKeys(tag, english)) {
    if (!absent.has(key)) translated++
    else {
      missing++
      issues.push({ level: 'error', locale: tag, area: english.get(key)?.area ?? english.get(`${splitPlural(key)!.base}_other`)!.area, key, message: 'missing translation' })
    }
  }

  return { issues, stats: { total: translated + missing, translated, missing } }
}

/** Dynamic key prefixes in the source: `tDynamic(\`status.${x}\`)` makes every `status.*` key used. */
function dynamicPrefixes(source: string): string[] {
  const out = new Set<string>()
  for (const m of source.matchAll(/`([\w.]+\.)\$\{/g)) out.add(m[1]!)
  for (const m of source.matchAll(/['"]([\w.]+\.)['"]\s*\+/g)) out.add(m[1]!)

  return [...out]
}

/** English keys no source file refers to: neither as a literal key (a plural group by its base key) nor under a dynamic prefix. */
export function findUnusedKeys(en: LocaleFiles, source: string): { area: string; key: string }[] {
  const prefixes = dynamicPrefixes(source)
  const unused: { area: string; key: string }[] = []
  for (const [key, { area }] of flatten(en)) {
    const base = splitPlural(key)?.base ?? key
    const quoted = (k: string) => source.includes(`'${k}'`) || source.includes(`"${k}"`) || source.includes(`\`${k}\``)
    if (quoted(key) || quoted(base) || prefixes.some((p) => key.startsWith(p))) continue
    unused.push({ area, key })
  }

  return unused
}

export function isPluralCategory(value: string): boolean {
  return CATEGORIES.has(value)
}
