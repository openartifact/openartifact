// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { LOCALE_STORAGE_KEY, initialLocale, saveLocale, storedLocale } from '../preference'

function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial))
  return {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  }
}

describe('language preference', () => {
  it('round-trips a saved choice and clears it with null', () => {
    const storage = memoryStorage()
    expect(storedLocale(storage)).toBeNull()
    saveLocale('ja', storage)
    expect(storage.getItem(LOCALE_STORAGE_KEY)).toBe('ja')
    expect(storedLocale(storage)).toBe('ja')
    saveLocale(null, storage)
    expect(storedLocale(storage)).toBeNull()
  })

  it('ignores a stored value that is not a supported tag', () => {
    expect(storedLocale(memoryStorage({ [LOCALE_STORAGE_KEY]: 'klingon' }))).toBeNull()
  })

  it('prefers the saved choice over the browser language', () => {
    expect(initialLocale(memoryStorage({ [LOCALE_STORAGE_KEY]: 'fr' }), ['de'])).toBe('fr')
    expect(initialLocale(memoryStorage(), ['de-DE', 'en'])).toBe('de')
  })

  it('survives storage that throws', () => {
    const broken = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
      removeItem: () => {
        throw new Error('denied')
      },
    }
    expect(storedLocale(broken)).toBeNull()
    expect(() => saveLocale('de', broken)).not.toThrow()
    expect(initialLocale(broken, ['it'])).toBe('it')
  })
})
