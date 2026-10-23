// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { request, ApiError } from '../api/client'
import { ErrorCodes } from '../api/error-codes'
import type { UserExtended } from '../api/generated/types.gen'
import type { ProjectMembership } from '../api/projects'
import { atLeast } from '../lib/projectAccess'
import { logout as apiLogout } from '../api/auth'
import { useToastStore } from './toast'
import { useUIStore } from './ui'
import { t } from '../i18n/en'

/** The signed-in user as `/auth/me` returns it, with the caller's project roles. */
export type User = UserExtended & { projects?: ProjectMembership[] }

export const useSessionStore = defineStore('session', () => {
  const ready = ref(false)
  const user = ref<User | null>(null)

  const isAuthenticated = computed(() => user.value !== null)

  const mustChangePassword = computed(() => user.value?.must_change_password === true)

  const isAdmin = computed(() => {
    if (!user.value) return false
    if (user.value.is_admin === true) return true
    if (user.value.groups?.some((g) => g.name === 'admin')) return true
    return false
  })

  /** Projects the caller holds a role in (no implicit admin entries). */
  const projects = computed<ProjectMembership[]>(() => user.value?.projects ?? [])

  /** Admins, and members holding maintainer or owner in some project, may create repositories. */
  const canCreateRepo = computed(
    () => isAdmin.value || projects.value.some((p) => atLeast(p.role, 'maintainer')),
  )

  // True when the last /auth/me probe failed for a reason other than "not
  // signed in" (network error, 5xx, proxy 502). The session state is then
  // unknown, not signed out: the router retries the probe on the next navigation.
  const probeFailed = ref(false)

  let inflight: Promise<void> | null = null

  async function probe(): Promise<void> {
    try {
      user.value = await request<User>('/api/v1/auth/me')
      probeFailed.value = false
    } catch (err) {
      if (
        err instanceof ApiError &&
        err.status === 403 &&
        (err.code === ErrorCodes.PASSWORD_CHANGE_REQUIRED || err.message === ErrorCodes.PASSWORD_CHANGE_REQUIRED)
      ) {
        // RBAC middleware blocks all routes when must_change_password is set,
        // including /auth/me. Treat that as "authenticated but locked".
        user.value = { id: '', username: '', must_change_password: true }
        probeFailed.value = false
      } else if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
        user.value = null
        probeFailed.value = false
      } else {
        // Transient failure: keep whatever user we already have (for example
        // the one the login response just returned) and flag the probe.
        if (!probeFailed.value) useToastStore().warning(t('session.probeFailed'), t('session.probeFailedHint'))
        probeFailed.value = true
      }
    } finally {
      ready.value = true
    }
  }

  /**
   * Refreshes the signed-in user from /auth/me. Never throws: a transient
   * failure sets `probeFailed` and still marks the session `ready` so the app
   * renders. Concurrent callers share one request.
   */
  function fetchMe(): Promise<void> {
    inflight ??= probe().finally(() => {
      inflight = null
    })
    return inflight
  }

  /** Drops the user and every other piece of per-user client state. */
  function clearSession(): void {
    user.value = null
    probeFailed.value = false
    useToastStore().clear()
    const ui = useUIStore()
    ui.closeMobileNav()
    ui.closeCommandPalette()
  }

  /**
   * Ends the server session, then clears the client state. Resolves to the
   * IdP end-session URL the caller must navigate to, if there is one. Throws
   * when the server could not be reached.
   */
  async function logout(): Promise<string | undefined> {
    let endSessionURL: string | undefined
    try {
      endSessionURL = await apiLogout()
    } catch (err) {
      // 401: the session is already gone server-side.
      if (!(err instanceof ApiError && err.status === 401)) throw err
    }
    clearSession()
    return endSessionURL
  }

  return { user, ready, isAuthenticated, isAdmin, canCreateRepo, mustChangePassword, projects, probeFailed, fetchMe, clearSession, logout }
})
