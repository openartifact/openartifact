// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

/**
 * Returns the one sign-out action shared by the sidebar, the command palette
 * and the forced password screen: end the server session, drop the per-user
 * client state and go to /login, or to the IdP's logout page when the session
 * came from an OIDC provider that supports RP-initiated logout (a full
 * navigation: the IdP page is outside the SPA). When the server cannot be reached the user
 * stays signed in and is told so, instead of looking signed out while the
 * session cookie is still valid.
 */
export function useSignOut(): () => Promise<void> {
  const router = useRouter()
  const session = useSessionStore()
  const toasts = useToastStore()

  return async () => {
    let endSessionURL: string | undefined
    try {
      endSessionURL = await session.logout()
    } catch (err) {
      toasts.error(t('auth.logoutFailed'), err instanceof Error ? err.message : undefined)
      return
    }

    if (endSessionURL) {
      window.location.assign(endSessionURL)
      return
    }
    await router.push('/login')
  }
}
