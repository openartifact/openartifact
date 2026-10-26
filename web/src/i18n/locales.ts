// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Locales the UI is translated into. `tag` is the BCP 47 tag used for the
 * catalog directory, `<html lang>` and every Intl call; `name` is the
 * language's own name (shown as is in the picker, never translated, so a
 * reader who landed in the wrong language can still find theirs).
 */
export const SUPPORTED_LOCALES = [
  { tag: 'en', name: 'English' },
  { tag: 'it', name: 'Italiano' },
  { tag: 'de', name: 'Deutsch' },
  { tag: 'fr', name: 'Français' },
  { tag: 'es', name: 'Español' },
  { tag: 'pt-BR', name: 'Português (Brasil)' },
  { tag: 'zh-Hans', name: '简体中文' },
  { tag: 'ja', name: '日本語' },
] as const

export type LocaleTag = (typeof SUPPORTED_LOCALES)[number]['tag']

/** The language of the source catalog: always bundled, the fallback of every other locale. */
export const SOURCE_LOCALE: LocaleTag = 'en'

const TAGS: readonly string[] = SUPPORTED_LOCALES.map((l) => l.tag)

export function isLocaleTag(value: unknown): value is LocaleTag {
  return typeof value === 'string' && TAGS.includes(value)
}

/** Primary language subtag → supported tag, for the languages with one translation. */
const BY_LANGUAGE: Readonly<Record<string, LocaleTag>> = {
  en: 'en',
  it: 'it',
  de: 'de',
  fr: 'fr',
  es: 'es',
  pt: 'pt-BR',
  ja: 'ja',
}

/** Scripts of the Chinese variants: only the simplified one is translated. */
function isSimplifiedChinese(tag: string): boolean {
  try {
    return new Intl.Locale(tag).maximize().script === 'Hans'
  } catch {
    return false
  }
}

function matchOne(language: string): LocaleTag | null {
  const normalized = language.trim().replace(/_/g, '-')
  const primary = normalized.split('-')[0]?.toLowerCase() ?? ''
  if (primary === 'zh') return isSimplifiedChinese(normalized) ? 'zh-Hans' : null

  return BY_LANGUAGE[primary] ?? null
}

/**
 * Picks the UI locale from the browser's preference list (`navigator.languages`):
 * the first entry with a translation wins. `pt` and `pt-PT` use Brazilian
 * Portuguese, `zh`, `zh-CN` and `zh-Hans` the simplified Chinese catalog;
 * traditional Chinese has none, so the next preferred language is tried.
 * Nothing matching gives English.
 */
export function resolveLocale(languages: readonly string[] | null | undefined): LocaleTag {
  for (const language of languages ?? []) {
    const hit = matchOne(language)
    if (hit) return hit
  }

  return SOURCE_LOCALE
}

const RTL_LANGUAGES = new Set(['ar', 'fa', 'he', 'ur', 'ps', 'yi', 'ckb', 'dv'])

/** Writing direction for `<html dir>`. */
export function localeDirection(tag: string): 'ltr' | 'rtl' {
  return RTL_LANGUAGES.has(tag.split('-')[0]?.toLowerCase() ?? '') ? 'rtl' : 'ltr'
}
