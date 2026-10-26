// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref } from 'vue'
import { currentLocale, setLocale, t } from '../i18n'
import { SUPPORTED_LOCALES, resolveLocale, type LocaleTag } from '../i18n/locales'
import { saveLocale, storedLocale } from '../i18n/preference'
import { useToastStore } from '../stores/toast'

/** What the picker stores: a language, or `auto` to follow the browser. */
export type LanguageChoice = 'auto' | LocaleTag

// One choice per page: the user menu and the login form show the same value.
const choice = ref<LanguageChoice>(storedLocale() ?? 'auto')

/**
 * The language picker's state. `choose` loads the catalog first and only then
 * switches (and saves, per browser, in localStorage), so a failed fetch leaves
 * the language and the saved choice untouched.
 */
export function useLanguage() {
  const toasts = useToastStore()

  async function choose(next: LanguageChoice): Promise<boolean> {
    const target = next === 'auto' ? resolveLocale(navigator.languages) : next
    try {
      await setLocale(target)
    } catch (err) {
      console.error('[i18n] could not load the language', err)
      toasts.error(t('shell.menu.languageFailed'))

      return false
    }
    saveLocale(next === 'auto' ? null : next)
    choice.value = next

    return true
  }

  return {
    choice,
    locale: computed(() => currentLocale.value),
    languages: SUPPORTED_LOCALES,
    choose,
  }
}
