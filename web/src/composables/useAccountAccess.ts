// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

/** Runs one change of an access panel: success toast and a reload; a failed request is an error toast and a reload too. */
export function useAccountAccess(reload: () => Promise<void>) {
  const toasts = useToastStore()

  return async function change(request: () => Promise<unknown>, success: string): Promise<void> {
    try {
      await request()
      toasts.success(success)
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
    }
    await reload()
  }
}
