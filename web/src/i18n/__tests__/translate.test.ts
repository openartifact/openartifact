// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { interpolate, lookup, pluralCategories, pluralMessage, type Catalog } from '../translate'

const english = {
  'a.title': 'Title',
  'a.only': 'English only',
  'n.item_one': '{n} item',
  'n.item_other': '{n} items',
}

const catalog = (tag: string, messages: Record<string, string>): Catalog => ({ tag, messages })

describe('interpolate', () => {
  it('fills known placeholders and leaves unknown ones as written', () => {
    expect(interpolate('Hello {name}, {missing}', { name: 'Ada' })).toBe('Hello Ada, {missing}')
  })
})

describe('lookup', () => {
  it('prefers the active catalog, falls back to English, then undefined', () => {
    const it = catalog('it', { 'a.title': 'Titolo' })
    expect(lookup(english, it, 'a.title')).toBe('Titolo')
    expect(lookup(english, it, 'a.only')).toBe('English only')
    expect(lookup(english, it, 'nope')).toBeUndefined()
  })
})

describe('pluralMessage by language rules', () => {
  const pairs = (one: string, other: string) => ({ 'n.item_one': one, 'n.item_other': other })

  it('English: one for 1, other for the rest', () => {
    const en = catalog('en', english)
    expect(pluralMessage(english, en, 'n.item', 1)).toBe('{n} item')
    expect(pluralMessage(english, en, 'n.item', 0)).toBe('{n} items')
    expect(pluralMessage(english, en, 'n.item', 2)).toBe('{n} items')
  })

  it('German: one for 1 only', () => {
    const de = catalog('de', pairs('{n} Element', '{n} Elemente'))
    expect(pluralMessage(english, de, 'n.item', 1)).toBe('{n} Element')
    expect(pluralMessage(english, de, 'n.item', 0)).toBe('{n} Elemente')
  })

  it('French and Brazilian Portuguese: zero counts as one', () => {
    const fr = catalog('fr', pairs('{n} élément', '{n} éléments'))
    expect(pluralMessage(english, fr, 'n.item', 0)).toBe('{n} élément')
    expect(pluralMessage(english, fr, 'n.item', 1)).toBe('{n} élément')
    expect(pluralMessage(english, fr, 'n.item', 2)).toBe('{n} éléments')
    const pt = catalog('pt-BR', pairs('{n} item', '{n} itens'))
    expect(pluralMessage(english, pt, 'n.item', 0)).toBe('{n} item')
    expect(pluralMessage(english, pt, 'n.item', 5)).toBe('{n} itens')
  })

  it('Italian and Spanish: one for 1; the many form of round millions falls back to other', () => {
    for (const tag of ['it', 'es']) {
      const c = catalog(tag, pairs('uno', 'altri'))
      expect(pluralMessage(english, c, 'n.item', 1)).toBe('uno')
      expect(pluralMessage(english, c, 'n.item', 2)).toBe('altri')
      expect(pluralMessage(english, c, 'n.item', 1_000_000)).toBe('altri')
    }
    const withMany = catalog('es', { ...pairs('uno', 'otros'), 'n.item_many': 'de millones' })
    expect(pluralMessage(english, withMany, 'n.item', 1_000_000)).toBe('de millones')
  })

  it('Chinese and Japanese: a single form for every count', () => {
    for (const tag of ['zh-Hans', 'ja']) {
      const c = catalog(tag, { 'n.item_other': '{n} 件' })
      for (const n of [0, 1, 2, 11]) expect(pluralMessage(english, c, 'n.item', n)).toBe('{n} 件')
    }
  })

  it('uses the English group with English rules when the locale has no entry', () => {
    const de = catalog('de', {})
    expect(pluralMessage(english, de, 'n.item', 1)).toBe('{n} item')
    expect(pluralMessage(english, de, 'n.item', 3)).toBe('{n} items')
  })

  it('does not mix languages when a locale only has the one form', () => {
    const partial = catalog('de', { 'n.item_one': '{n} Element' })
    expect(pluralMessage(english, partial, 'n.item', 1)).toBe('{n} item')
  })

  it('is undefined for an unknown group', () => {
    expect(pluralMessage(english, catalog('en', english), 'x.y', 1)).toBeUndefined()
  })
})

describe('pluralCategories', () => {
  it('lists the CLDR categories of each shipped language', () => {
    expect([...pluralCategories('en')].sort()).toEqual(['one', 'other'])
    expect([...pluralCategories('de')].sort()).toEqual(['one', 'other'])
    expect([...pluralCategories('ja')]).toEqual(['other'])
    expect([...pluralCategories('zh-Hans')]).toEqual(['other'])
    for (const tag of ['it', 'es', 'fr', 'pt-BR']) expect(pluralCategories(tag)).toContain('one')
    for (const tag of ['it', 'es', 'fr', 'pt-BR']) expect(pluralCategories(tag)).toContain('other')
  })
})
