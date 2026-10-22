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

  async function fetchMe(): Promise<void> {
    try {
      user.value = await request<User>('/api/v1/auth/me')
    } catch (err) {
      if (
        err instanceof ApiError &&
        err.status === 403 &&
        (err.code === ErrorCodes.PASSWORD_CHANGE_REQUIRED || err.message === ErrorCodes.PASSWORD_CHANGE_REQUIRED)
      ) {
        // RBAC middleware blocks all routes when must_change_password is set,
        // including /auth/me. Treat that as "authenticated but locked".
        user.value = { id: '', username: '', must_change_password: true }
      } else {
        user.value = null
      }
    } finally {
      ready.value = true
    }
  }

  function clearSession(): void {
    user.value = null
  }

  return { user, ready, isAuthenticated, isAdmin, canCreateRepo, mustChangePassword, projects, fetchMe, clearSession }
})
