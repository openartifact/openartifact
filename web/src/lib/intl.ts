// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { currentLocale } from '../i18n'

/**
 * Locale of every date, time, number, list and sort rendering. It is the UI
 * language (the catalog in use), never the browser's, so numbers and dates
 * always match the text around them.
 */
export function formatLocale(): string {
  return currentLocale.value
}

/** Integer or decimal with the active language's digit grouping. */
export function formatNumber(n: number | null | undefined, options?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(formatLocale(), options).format(n ?? 0)
}

const collators = new Map<string, Intl.Collator>()

/** Collator of the active language (cached per language). */
export function collator(): Intl.Collator {
  const tag = formatLocale()
  let c = collators.get(tag)
  if (!c) {
    c = new Intl.Collator(tag, { sensitivity: 'base', numeric: true })
    collators.set(tag, c)
  }

  return c
}

/**
 * Compares two display strings by the active language's alphabet (accents and
 * case do not decide the order, digit runs compare as numbers). For sorting
 * names a user reads; ISO timestamps and ids compare with plain operators.
 */
export function compareText(a: string | null | undefined, b: string | null | undefined): number {
  return collator().compare(a ?? '', b ?? '')
}
