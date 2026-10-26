// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { SUPPORTED_LOCALES, isLocaleTag, localeDirection, resolveLocale } from '../locales'

describe('resolveLocale', () => {
  it('maps every Portuguese variant to Brazilian Portuguese', () => {
    expect(resolveLocale(['pt-BR'])).toBe('pt-BR')
    expect(resolveLocale(['pt'])).toBe('pt-BR')
    expect(resolveLocale(['pt-PT'])).toBe('pt-BR')
  })

  it('maps simplified Chinese spellings to zh-Hans', () => {
    for (const tag of ['zh-CN', 'zh-Hans', 'zh', 'zh-SG', 'zh-Hans-CN']) expect(resolveLocale([tag])).toBe('zh-Hans')
  })

  it('skips traditional Chinese and tries the next preference', () => {
    expect(resolveLocale(['zh-TW'])).toBe('en')
    expect(resolveLocale(['zh-Hant', 'zh-HK', 'de-CH'])).toBe('de')
  })

  it('uses the primary language of a regional tag', () => {
    expect(resolveLocale(['de-AT'])).toBe('de')
    expect(resolveLocale(['fr-CA'])).toBe('fr')
    expect(resolveLocale(['es-MX'])).toBe('es')
    expect(resolveLocale(['it-CH'])).toBe('it')
    expect(resolveLocale(['ja-JP'])).toBe('ja')
    expect(resolveLocale(['EN-gb'])).toBe('en')
  })

  it('takes the first preferred language that has a translation', () => {
    expect(resolveLocale(['ko', 'fr-FR', 'de'])).toBe('fr')
  })

  it('falls back to English for unknown, empty or malformed input', () => {
    expect(resolveLocale(['ko', 'ru'])).toBe('en')
    expect(resolveLocale([])).toBe('en')
    expect(resolveLocale(undefined)).toBe('en')
    expect(resolveLocale(['', '!!', 'zh-@@'])).toBe('en')
  })

  it('accepts underscore separators', () => {
    expect(resolveLocale(['pt_BR'])).toBe('pt-BR')
    expect(resolveLocale(['zh_CN'])).toBe('zh-Hans')
  })
})

describe('locale tags', () => {
  it('knows exactly the supported tags', () => {
    expect(SUPPORTED_LOCALES.map((l) => l.tag)).toEqual(['en', 'it', 'de', 'fr', 'es', 'pt-BR', 'zh-Hans', 'ja'])
    expect(isLocaleTag('pt-BR')).toBe(true)
    expect(isLocaleTag('pt')).toBe(false)
    expect(isLocaleTag(null)).toBe(false)
  })

  it('names every language in itself', () => {
    expect(SUPPORTED_LOCALES.find((l) => l.tag === 'de')?.name).toBe('Deutsch')
    expect(SUPPORTED_LOCALES.find((l) => l.tag === 'ja')?.name).toBe('日本語')
  })

  it('writes left to right for all of them', () => {
    for (const { tag } of SUPPORTED_LOCALES) expect(localeDirection(tag)).toBe('ltr')
    expect(localeDirection('ar')).toBe('rtl')
    expect(localeDirection('he-IL')).toBe('rtl')
  })
})
