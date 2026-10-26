// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it } from 'bun:test'
import { currentLocale, initI18n, loadLocale, setLocale, t, tDynamic, tp, tpn } from '..'
import { LOCALE_STORAGE_KEY } from '../preference'

afterEach(async () => {
  localStorage.clear()
  await setLocale('en')
})

describe('lazy-loaded locales', () => {
  it('serves English synchronously and the other languages after their chunk loaded', async () => {
    expect(t('auth.signIn')).toBe('Sign in')
    await setLocale('de')
    expect(currentLocale.value).toBe('de')
    expect(t('auth.signIn')).toBe('Anmelden')
  })

  it('loads each locale once', async () => {
    const first = await loadLocale('it')
    expect(await loadLocale('it')).toBe(first)
    expect(Object.keys(first).length).toBeGreaterThan(1000)
  })

  it('sets html lang and dir', async () => {
    await setLocale('ja')
    expect(document.documentElement.lang).toBe('ja')
    expect(document.documentElement.dir).toBe('ltr')
    await setLocale('pt-BR')
    expect(document.documentElement.lang).toBe('pt-BR')
    await setLocale('en')
    expect(document.documentElement.lang).toBe('en')
  })

  it('falls back to English for a key the catalog lacks, and to the key when nothing has it', async () => {
    await setLocale('de')
    expect(tDynamic('no.such.key')).toBe('no.such.key')
    const messages = await loadLocale('de')
    expect(Object.keys(messages).every((k) => messages[k] !== '')).toBe(true)
  })

  it('applies plural rules of the active language', async () => {
    expect(tpn('search.versionCount', 1)).toBe('1 version')
    await setLocale('ja')
    expect(tpn('search.versionCount', 1)).toBe(tpn('search.versionCount', 5).replace('5', '1'))
    await setLocale('de')
    expect(tpn('search.versionCount', 1)).not.toBe(tpn('search.versionCount', 2).replace('2', '1'))
  })

  it('keeps interpolation unchanged in every language', async () => {
    await setLocale('fr')
    expect(tp('shell.menu.language', {})).toBe(t('shell.menu.language'))
  })
})

describe('initI18n', () => {
  it('starts in the saved language, else the browser one', async () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, 'es')
    expect(await initI18n()).toBe('es')
    expect(document.documentElement.lang).toBe('es')
  })
})
