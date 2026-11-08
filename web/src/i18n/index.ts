// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The i18n runtime. English (locales/en/*.json, the source of truth) is bundled
// and types `t(key)`; every other locale is a separate chunk fetched by
// `setLocale` before the UI renders in it. After that load `t()` is
// synchronous and reads reactive state, so a template or computed that called
// it re-renders when the language changes. A key a locale lacks falls back to
// English.

import { computed, shallowRef } from 'vue'
import { SOURCE_LOCALE, localeDirection, type LocaleTag } from './locales'
import { initialLocale } from './preference'
import { interpolate, lookup, pluralMessage, type Catalog, type Messages, type Params } from './translate'
import admin from './locales/en/admin.json'
import artifact from './locales/en/artifact.json'
import auth from './locales/en/auth.json'
import branding from './locales/en/branding.json'
import bundles from './locales/en/bundles.json'
import core from './locales/en/core.json'
import dashboard from './locales/en/dashboard.json'
import federation from './locales/en/federation.json'
import projects from './locales/en/projects.json'
import repolist from './locales/en/repolist.json'
import repos from './locales/en/repos.json'
import search from './locales/en/search.json'
import security from './locales/en/security.json'
import settings from './locales/en/settings.json'
import settingsx from './locales/en/settingsx.json'
import shell from './locales/en/shell.json'
import tiering from './locales/en/tiering.json'
import tokens from './locales/en/tokens.json'
import ui from './locales/en/ui.json'

const english = {
  ...admin,
  ...artifact,
  ...auth,
  ...branding,
  ...bundles,
  ...core,
  ...dashboard,
  ...federation,
  ...projects,
  ...repolist,
  ...repos,
  ...search,
  ...security,
  ...settings,
  ...settingsx,
  ...shell,
  ...tiering,
  ...tokens,
  ...ui,
} satisfies Record<string, string>

export type TranslationKey = keyof typeof english

/** Base keys of the plural groups: `foo` for `foo_one` + `foo_other`. */
export type PluralKey = { [K in TranslationKey]: K extends `${infer Base}_other` ? Base : never }[TranslationKey]

const englishMessages: Messages = english

const active = shallowRef<Catalog>({ tag: SOURCE_LOCALE, messages: englishMessages })

/** The locale the UI is rendered in (reactive). */
export const currentLocale = computed(() => active.value.tag as LocaleTag)

/** Typed lookup: an unknown key is a compile error. */
export function t(key: TranslationKey): string {
  return lookup(englishMessages, active.value, key) ?? key
}

/**
 * Typed lookup with `{name}` placeholders filled from `params`. Use it for
 * strings that carry the product name or other runtime values, instead of
 * concatenating translated fragments.
 */
export function tp(key: TranslationKey, params: Params): string {
  return interpolate(t(key), localize(params))
}

/** Numbers become text with the active language's digit grouping; strings pass through. */
function localize(params: Params): Params {
  const format = new Intl.NumberFormat(active.value.tag)
  return Object.fromEntries(Object.entries(params).map(([k, v]) => [k, typeof v === 'number' ? format.format(v) : v]))
}

/**
 * Plural-aware `tp`: the count selects the `<key>_<category>` variant by the
 * plural rules of the active language (one-form languages such as Japanese
 * only carry `_other`). `{n}` and `{count}` are both filled with the count;
 * `params` adds further placeholders.
 */
export function tpn(key: PluralKey, count: number, params: Params = {}): string {
  const message = pluralMessage(englishMessages, active.value, key, count) ?? key

  return interpolate(message, localize({ n: count, count, ...params }))
}

/** Lookup for keys assembled at runtime (e.g. `status.${state}`); falls back to the key itself. */
export function tDynamic(key: string): string {
  return lookup(englishMessages, active.value, key) ?? key
}

const loaded = new Map<LocaleTag, Messages>()
const loading = new Map<LocaleTag, Promise<Messages>>()

/** Every locale but English, the bundled source. */
type TranslatedTag = Exclude<LocaleTag, 'en'>

/**
 * One loader per translated language: its whole catalog is one module (`catalogs/locale-<tag>.ts`),
 * hence one chunk and one request, fetched only for a user of that language. English is bundled
 * statically above.
 */
const LOADERS: Readonly<Record<TranslatedTag, () => Promise<{ default: Messages }>>> = {
  it: () => import('./catalogs/locale-it'),
  de: () => import('./catalogs/locale-de'),
  fr: () => import('./catalogs/locale-fr'),
  es: () => import('./catalogs/locale-es'),
  'pt-BR': () => import('./catalogs/locale-pt-BR'),
  'zh-Hans': () => import('./catalogs/locale-zh-Hans'),
  ja: () => import('./catalogs/locale-ja'),
  ko: () => import('./catalogs/locale-ko'),
  pl: () => import('./catalogs/locale-pl'),
  uk: () => import('./catalogs/locale-uk'),
}

/** Loads the catalog of `tag` (once; English is always present). Rejects when the chunk cannot be fetched. */
export function loadLocale(tag: LocaleTag): Promise<Messages> {
  if (tag === 'en') return Promise.resolve(englishMessages)
  const done = loaded.get(tag)
  if (done) return Promise.resolve(done)
  let pending = loading.get(tag)
  if (!pending) {
    pending = LOADERS[tag]()
      .then(({ default: messages }) => {
        loaded.set(tag, messages)
        return messages
      })
      .finally(() => loading.delete(tag))
    loading.set(tag, pending)
  }

  return pending
}

function applyDocumentLocale(tag: LocaleTag): void {
  if (typeof document === 'undefined') return
  document.documentElement.lang = tag
  document.documentElement.dir = localeDirection(tag)
}

/**
 * Makes `tag` the active language: loads its catalog first, so the UI never
 * renders half in the old language, then switches `t()` and `<html lang dir>`.
 * On a load failure the current language stays and the promise rejects.
 */
export async function setLocale(tag: LocaleTag): Promise<void> {
  const messages = await loadLocale(tag)
  active.value = { tag, messages }
  applyDocumentLocale(tag)
}

/**
 * Start-up: picks the saved or browser language and activates it before the app
 * mounts. A catalog that cannot be fetched leaves the UI in English.
 */
export async function initI18n(): Promise<LocaleTag> {
  try {
    await setLocale(initialLocale())
  } catch (err) {
    console.error('[i18n] could not load the language, using English', err)
    applyDocumentLocale(SOURCE_LOCALE)
  }

  return currentLocale.value
}
