// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { pluralCategories, pluralMessage, type Messages } from '../translate'

const LOCALES = join(import.meta.dir, '..', 'locales')

function catalog(tag: string): Messages {
  const dir = join(LOCALES, tag)
  const out: Record<string, string> = {}
  for (const f of readdirSync(dir).filter((n) => n.endsWith('.json'))) Object.assign(out, JSON.parse(readFileSync(join(dir, f), 'utf8')))

  return out
}

/** Plural groups of a catalog: base key → the categories it has. */
function groups(messages: Messages): Map<string, Set<string>> {
  const out = new Map<string, Set<string>>()
  for (const key of Object.keys(messages)) {
    const m = /^(.+)_(zero|one|two|few|many|other)$/.exec(key)
    if (m) out.set(m[1]!, (out.get(m[1]!) ?? new Set()).add(m[2]!))
  }

  return out
}

// `i18n:check` only asks for the forms English has (`one`, `other`); Polish and Ukrainian need
// `few` and `many` as well, or 2-4 and 5+ would read as the wrong grammatical number.
describe('plural forms of the shipped languages', () => {
  const english = groups(catalog('en'))

  it('Polish and Ukrainian give every group all four CLDR forms', () => {
    for (const tag of ['pl', 'uk']) {
      expect([...pluralCategories(tag)].sort()).toEqual(['few', 'many', 'one', 'other'])
      const own = groups(catalog(tag))
      const incomplete = [...english.keys()].filter((base) => ['one', 'few', 'many', 'other'].some((c) => !own.get(base)?.has(c)))
      expect(incomplete, `${tag}: plural groups lacking a form`).toEqual([])
    }
  })

  it('Korean, Japanese and Chinese only have the "other" form', () => {
    for (const tag of ['ko', 'ja', 'zh-Hans']) {
      const forms = [...groups(catalog(tag)).values()].flatMap((s) => [...s])
      expect(new Set(forms), tag).toEqual(new Set(['other']))
    }
  })

  it('picks the Polish and Ukrainian form by CLDR rule', () => {
    const en = catalog('en')
    for (const tag of ['pl', 'uk']) {
      const messages = catalog(tag)
      const active = { tag, messages }
      const pick = (n: number) => pluralMessage(en, active, 'search.versionCount', n)
      const forms = (n: number) => messages[`search.versionCount_${new Intl.PluralRules(tag).select(n)}`]
      for (const n of [1, 2, 4, 5, 12, 22, 25]) expect(pick(n), `${tag} ${n}`).toBe(forms(n))
      expect(pick(2)).not.toBe(pick(5))
    }
  })
})
