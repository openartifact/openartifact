// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { checkLocale, checkSource, findUnusedKeys, placeholdersOf, splitPlural } from '../catalogCheck'

const en = {
  a: {
    'a.hello': 'Hello {name}',
    'a.plain': 'Plain',
    'a.count_one': '{n} file',
    'a.count_other': '{n} files',
  },
  b: { 'b.other': 'Other' },
}

const errors = (r: { issues: { level: string; key: string; message: string }[] }) => r.issues.filter((i) => i.level === 'error')

describe('placeholders and plural keys', () => {
  it('lists placeholders sorted and unique', () => {
    expect(placeholdersOf('{b} and {a} and {b}')).toEqual(['a', 'b'])
    expect(placeholdersOf('no placeholders')).toEqual([])
  })

  it('splits plural suffixes', () => {
    expect(splitPlural('a.count_one')).toEqual({ base: 'a.count', category: 'one' })
    expect(splitPlural('a.count')).toBeNull()
    expect(splitPlural('a.some_thing')).toBeNull()
  })
})

describe('checkLocale', () => {
  it('passes a complete, correct translation', () => {
    const r = checkLocale('de', en, {
      a: { 'a.hello': 'Hallo {name}', 'a.plain': 'Schlicht', 'a.count_one': '{n} Datei', 'a.count_other': '{n} Dateien' },
      b: { 'b.other': 'Anderes' },
    })
    expect(r.issues).toEqual([])
    expect(r.stats).toEqual({ total: 4, translated: 4, missing: 0 })
  })

  it('fails on a key English does not have', () => {
    const r = checkLocale('de', en, { a: { 'a.extra': 'x' } })
    expect(errors(r).map((i) => i.key)).toEqual(['a.extra'])
  })

  it('fails on a key that lives in another file in English', () => {
    const r = checkLocale('de', en, { b: { 'a.plain': 'Schlicht' } })
    expect(errors(r)[0]?.message).toContain('a.json')
  })

  it('fails on a placeholder that differs, is renamed or is dropped', () => {
    expect(errors(checkLocale('de', en, { a: { 'a.hello': 'Hallo {nom}' } }))).toHaveLength(1)
    expect(errors(checkLocale('de', en, { a: { 'a.hello': 'Hallo' } }))).toHaveLength(1)
    expect(errors(checkLocale('de', en, { a: { 'a.hello': 'Hallo {name} {name}' } }))).toHaveLength(0)
  })

  it('fails on a plural form the language does not use', () => {
    const ja = checkLocale('ja', en, { a: { 'a.count_one': '{n} 件', 'a.count_other': '{n} 件' } })
    expect(errors(ja).map((i) => i.key)).toEqual(['a.count_one'])
    const de = checkLocale('de', en, { a: { 'a.count_one': '{n}', 'a.count_other': '{n}', 'a.count_few': '{n}' } })
    expect(errors(de).map((i) => i.key)).toEqual(['a.count_few'])
  })

  it('accepts the extra forms a language has, checked against the English other form', () => {
    const fr = checkLocale('fr', en, { a: { 'a.count_one': '{n} fichier', 'a.count_many': '{n} de fichiers', 'a.count_other': '{n} fichiers' } })
    expect(errors(fr)).toEqual([])
    const bad = checkLocale('fr', en, { a: { 'a.count_many': 'fichiers', 'a.count_other': '{n} fichiers' } })
    expect(errors(bad).map((i) => i.key)).toEqual(['a.count_many'])
  })

  it('fails on a plural group without other', () => {
    const r = checkLocale('de', en, { a: { 'a.count_one': '{n} Datei' } })
    expect(errors(r).map((i) => i.message)).toEqual(['plural group has no "other" form'])
  })

  it('reports missing and empty keys as warnings, not errors', () => {
    const r = checkLocale('de', en, { a: { 'a.hello': '', 'a.plain': 'Schlicht' } })
    expect(errors(r)).toEqual([])
    expect(r.issues.filter((i) => i.level === 'warn').map((i) => i.key).sort()).toEqual(['a.count_other', 'a.hello', 'b.other'])
    expect(r.stats).toEqual({ total: 4, translated: 1, missing: 3 })
  })

  it('fails on non-string values', () => {
    const r = checkLocale('de', en, { a: { 'a.plain': 5 as unknown as string } })
    expect(errors(r)).toHaveLength(1)
  })
})

describe('checkSource', () => {
  it('requires one and other in every English plural group', () => {
    expect(checkSource(en)).toEqual([])
    const broken = checkSource({ a: { 'a.n_other': '{n}' } })
    expect(broken.map((i) => i.key)).toEqual(['a.n_one'])
  })
})

describe('findUnusedKeys', () => {
  const source = `t('a.hello'); tpn('a.count', 2); tDynamic(\`dyn.\${x}\`)`
  it('counts literal keys, plural bases and dynamic prefixes as used', () => {
    const catalog = { a: { ...en.a, 'dyn.one': 'x', 'dyn.two': 'y', 'a.dead': 'z' } }
    expect(findUnusedKeys(catalog, source)).toEqual([{ area: 'a', key: 'a.plain' }, { area: 'a', key: 'a.dead' }])
  })
})
