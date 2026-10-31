// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, type ComputedRef } from 'vue'
import { useRouter } from 'vue-router'
import { copyText } from '../lib/clipboard'
import type { ActionEnv, PaletteContext } from '../lib/paletteActions'
import { useSessionStore } from '../stores/session'
import { useThemeStore } from '../stores/theme'
import { useToastStore } from '../stores/toast'
import { useLanguage } from './useLanguage'
import { useSignOut } from './useSignOut'

/**
 * What the palette's action registry (`lib/paletteActions.ts`) runs on: the
 * caller as the permission predicates see it, and the side effects (navigate,
 * copy, switch theme or language, sign out) wired to the app's stores.
 * `canCreateProject` is the API's `can_create` for self-service project creation.
 */
export function usePaletteActions(canCreateProject: () => boolean): { context: ComputedRef<PaletteContext>; env: ActionEnv } {
  const router = useRouter()
  const session = useSessionStore()
  const theme = useThemeStore()
  const toasts = useToastStore()
  const language = useLanguage()
  const signOut = useSignOut()

  const context = computed<PaletteContext>(() => ({
    isAdmin: session.isAdmin,
    canCreateRepo: session.canCreateRepo,
    canCreateProject: session.isAdmin || canCreateProject(),
    projectRoles: Object.fromEntries(session.projects.map((p) => [p.name, p.role])),
    theme: theme.resolved,
    language: language.choice.value,
  }))

  const env: ActionEnv = {
    origin: window.location.origin,
    go: (path) => router.push(path),
    copy: copyText,
    toggleTheme: () => theme.setMode(theme.resolved === 'dark' ? 'light' : 'dark'),
    setLanguage: language.choose,
    signOut,
    toast: {
      success: (title) => void toasts.success(title),
      error: (title, detail) => void toasts.error(title, detail),
    },
  }

  return { context, env }
}
