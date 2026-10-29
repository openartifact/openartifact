// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref } from 'vue'
import { changePassword } from '../api/auth'
import { t } from '../i18n'

const MIN_PASSWORD_LENGTH = 12

function emptyForm(): { current_password: string; new_password: string; confirm_password: string } {
  return { current_password: '', new_password: '', confirm_password: '' }
}

/**
 * The change-password form shared by the forced change at login and the account
 * settings: field state, client-side checks and the request. The caller decides
 * what a success and a failure look like.
 */
export function usePasswordChange() {
  const form = ref(emptyForm())
  const errors = ref<Record<string, string>>({})
  const loading = ref(false)

  /** Field errors for the current form; empty when it can be sent. */
  function validate(): Record<string, string> {
    const f = form.value
    if (f.new_password.length < MIN_PASSWORD_LENGTH) return { new_password: t('auth.passwordMinError') }
    if (f.new_password !== f.confirm_password) return { confirm_password: t('auth.passwordMismatch') }
    if (f.new_password === f.current_password) return { new_password: t('auth.passwordSame') }
    return {}
  }

  function reset(): void {
    form.value = emptyForm()
  }

  /**
   * Checks the form, changes the password, then runs `onChanged`. A failure of the request or of `onChanged`
   * goes to `onError` with its message.
   */
  async function submit(onChanged: () => Promise<void> | void, onError: (message: string) => void): Promise<void> {
    errors.value = validate()
    if (Object.keys(errors.value).length > 0) return
    loading.value = true
    try {
      await changePassword({ current_password: form.value.current_password, new_password: form.value.new_password })
      await onChanged()
    } catch (err: unknown) {
      onError(err instanceof Error ? err.message : t('auth.passwordChangeFailed'))
    } finally {
      loading.value = false
    }
  }

  return { form, errors, loading, reset, submit }
}
