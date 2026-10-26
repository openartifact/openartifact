// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure lookup, interpolation and plural selection over a message map. The
// reactive wiring (which catalog is active) lives in index.ts; keeping these
// free of state lets the tests exercise them with synthetic catalogs.

export type Messages = Readonly<Record<string, string>>

export interface Catalog {
  /** BCP 47 tag: selects the plural rules. */
  readonly tag: string
  readonly messages: Messages
}

export type Params = Readonly<Record<string, string>>

const PLACEHOLDER = /\{(\w+)\}/g

/** Fills `{name}` placeholders; an unknown name stays as written. */
export function interpolate(message: string, params: Params): string {
  return message.replace(PLACEHOLDER, (match, name: string) => params[name] ?? match)
}

/** The message of the active locale, else the English one; undefined when neither has the key. */
export function lookup(english: Messages, active: Catalog, key: string): string | undefined {
  return active.messages[key] ?? english[key]
}

const rulesCache = new Map<string, Intl.PluralRules>()

function pluralRules(tag: string): Intl.PluralRules {
  let rules = rulesCache.get(tag)
  if (!rules) {
    rules = new Intl.PluralRules(tag)
    rulesCache.set(tag, rules)
  }

  return rules
}

/**
 * Plural variants live as `<key>_<category>` (the i18next v4 convention):
 * zero, one, two, few, many, other, named by CLDR for the language. The
 * category of `count` picks the variant, `_other` is the catch-all. A locale
 * that has no `<key>_other` at all falls back to the whole English group, with
 * English rules, so a half-translated group never mixes languages.
 */
export function pluralMessage(english: Messages, active: Catalog, key: string, count: number): string | undefined {
  const otherKey = `${key}_other`
  if (active.messages[otherKey] !== undefined) {
    const category = pluralRules(active.tag).select(count)
    return active.messages[`${key}_${category}`] ?? active.messages[otherKey]
  }
  if (english[otherKey] === undefined) return undefined
  const category = pluralRules('en').select(count)

  return english[`${key}_${category}`] ?? english[otherKey]
}

/** Plural categories CLDR defines for a locale (what a catalog may carry as `_<category>` suffixes). */
export function pluralCategories(tag: string): readonly string[] {
  return pluralRules(tag).resolvedOptions().pluralCategories
}
