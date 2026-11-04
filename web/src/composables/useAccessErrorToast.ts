// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, type ComputedRef } from 'vue'
import type { Project } from '../api/projects'
import { errorStatus } from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

/**
 * Returns a function that toasts why a member or group change failed: the text
 * for its status, "forbidden" for a 403, else `fallback`.
 */
export function useAccessErrorToast(): (err: unknown, fallback: string, byStatus?: Record<number, string>) => void {
  const toasts = useToastStore()
  return (err, fallback, byStatus = {}) => {
    const status = errorStatus(err)
    toasts.error((status !== undefined ? byStatus[status] : undefined) ?? (status === 403 ? t('projects.members.forbidden') : fallback))
  }
}

/** What the member and group sections of a project share: toasts, the project name and the caller's role in it. */
export function useProjectAccess(project: () => Project) {
  return {
    toasts: useToastStore(),
    toastAccessError: useAccessErrorToast(),
    name: computed(() => project().name ?? ''),
    myRole: computed(() => project().my_role) as ComputedRef<Project['my_role']>,
  }
}
