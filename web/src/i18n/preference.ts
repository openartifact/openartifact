// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { isLocaleTag, resolveLocale, type LocaleTag } from './locales'

/** localStorage key of the language chosen in the user menu; absent means "follow the browser". */
export const LOCALE_STORAGE_KEY = 'oa.locale'

type StorageLike = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

function defaultStorage(): StorageLike | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

/** The saved choice, or null when the user never picked one (or storage is unavailable). */
export function storedLocale(storage: StorageLike | null = defaultStorage()): LocaleTag | null {
  try {
    const raw = storage?.getItem(LOCALE_STORAGE_KEY)
    return isLocaleTag(raw) ? raw : null
  } catch {
    return null
  }
}

/** Saves the choice; `null` clears it so the browser language applies again. */
export function saveLocale(tag: LocaleTag | null, storage: StorageLike | null = defaultStorage()): void {
  try {
    if (tag === null) storage?.removeItem(LOCALE_STORAGE_KEY)
    else storage?.setItem(LOCALE_STORAGE_KEY, tag)
  } catch {
    // Private mode or a full quota: the choice then lasts until the page closes.
  }
}

/** Locale at start-up: the saved choice, else the browser's preference list. */
export function initialLocale(
  storage: StorageLike | null = defaultStorage(),
  languages: readonly string[] | undefined = typeof navigator === 'undefined' ? undefined : navigator.languages,
): LocaleTag {
  return storedLocale(storage) ?? resolveLocale(languages)
}
