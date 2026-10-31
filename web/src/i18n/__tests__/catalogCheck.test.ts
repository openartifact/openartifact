// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { checkLocale, checkSource, findUnusedKeys, mergeCatalog, missingEntries, placeholdersOf, splitPlural } from '../catalogCheck'

const en = {
  a: {
    'a.hello': 'Hello {name}',
    'a.plain': 'Plain',
    'a.count_one': '{n} file',
    'a.count_other': '{n} files',
  },
  b: { 'b.other': 'Other' },
}

// partial catalogs in the tests below: gaps are covered by their own tests
const errors = (r: { issues: { level: string; key: string; message: string }[] }) =>
  r.issues.filter((i) => i.level === 'error' && i.message !== 'missing translation')

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
    expect(r.stats).toEqual({ total: 5, translated: 5, missing: 0 })
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

  it('fails on missing and empty keys', () => {
    const r = checkLocale('de', en, { a: { 'a.hello': '', 'a.plain': 'Schlicht' } })
    expect(r.issues.filter((i) => i.message === 'missing translation').map((i) => `${i.level}:${i.key}`).sort()).toEqual([
      'error:a.count_one',
      'error:a.count_other',
      'error:a.hello',
      'error:b.other',
    ])
    expect(r.stats).toEqual({ total: 5, translated: 1, missing: 4 })
  })

  it('does not require plural forms the language does not use', () => {
    const r = checkLocale('ja', en, { a: { 'a.hello': 'x {name}', 'a.plain': 'x', 'a.count_other': '{n}' }, b: { 'b.other': 'x' } })
    expect(r.issues).toEqual([])
    expect(r.stats).toEqual({ total: 4, translated: 4, missing: 0 })
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

describe('missingEntries', () => {
  it('lists required keys in English order with the English text', () => {
    expect(missingEntries('de', en, { a: { 'a.plain': 'Schlicht', 'a.count_other': '' } })).toEqual([
      { area: 'a', key: 'a.hello', source: 'Hello {name}' },
      { area: 'a', key: 'a.count_one', source: '{n} file' },
      { area: 'a', key: 'a.count_other', source: '{n} files' },
      { area: 'b', key: 'b.other', source: 'Other' },
    ])
    expect(missingEntries('ja', en, {}).map((m) => m.key)).toEqual(['a.hello', 'a.plain', 'a.count_other', 'b.other'])
  })
})

describe('mergeCatalog', () => {
  const source = en.a

  it('orders keys like English and keeps extra plural forms after other', () => {
    const merged = mergeCatalog(source, { 'a.count_many': '{n} de', 'a.plain': 'Schlicht' }, { 'a.hello': 'Hallo {name}', 'a.count_other': '{n} Dateien' })
    expect(Object.keys(merged)).toEqual(['a.hello', 'a.plain', 'a.count_other', 'a.count_many'])
  })

  it('accepts a plural form English lacks', () => {
    expect(mergeCatalog(source, {}, { 'a.count_few': '{n} x' })['a.count_few']).toBe('{n} x')
  })

  it('rejects unknown keys, empty values and changed placeholders', () => {
    expect(() => mergeCatalog(source, {}, { 'a.nope': 'x' })).toThrow('not in the English catalog')
    expect(() => mergeCatalog(source, {}, { 'a.plain': '' })).toThrow('empty')
    expect(() => mergeCatalog(source, {}, { 'a.hello': 'Hallo {nom}' })).toThrow('placeholders')
  })
})
