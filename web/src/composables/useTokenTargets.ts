// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, watch, type Ref } from 'vue'
import { listPermissions } from '../api/admin'
import { listProjects, SYSTEM_PROJECT_NAME, type Project } from '../api/projects'
import { listAllRepositories } from '../api/repositories'
import { t } from '../i18n/en'

export interface PermissionChoice {
  id: string
  name: string
}

/**
 * What the create drawer can point a token at, loaded the first time the
 * drawer opens: projects the caller can mint for, and for administrators the
 * federated hosted repositories and the permission targets a personal token
 * can be limited to. A failed part leaves its list empty and sets `error`; the
 * others still work.
 */
export function useTokenTargets(open: Ref<boolean>, isAdmin: boolean) {
  const projects = ref<Project[]>([])
  const repositories = ref<string[]>([])
  const permissions = ref<PermissionChoice[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref('')

  async function load(): Promise<void> {
    loading.value = true
    error.value = ''
    const failures: string[] = []
    const remember = (err: unknown): void => {
      failures.push(err instanceof Error ? err.message : String(err))
    }
    await Promise.all([
      listProjects({ all: isAdmin })
        .then((res) => {
          projects.value = res.projects.filter((p) => p.name && p.name !== SYSTEM_PROJECT_NAME)
        })
        .catch(remember),
      isAdmin
        ? listAllRepositories()
            .then((all) => {
              repositories.value = all.filter((r) => r.repo_type === 'hosted' && r.federated === true && r.name).map((r) => r.name as string)
            })
            .catch(remember)
        : Promise.resolve(),
      isAdmin
        ? listPermissions({ limit: 100 })
            .then((res) => {
              permissions.value = res.items.filter((p) => p.id && p.name).map((p) => ({ id: p.id as string, name: p.name as string }))
            })
            .catch(remember)
        : Promise.resolve(),
    ])
    if (failures.length > 0) error.value = t('tok.create.targetsFailed')
    loading.value = false
    loaded.value = true
  }

  watch(
    open,
    (isOpen) => {
      if (isOpen && !loaded.value && !loading.value) void load()
    },
    { immediate: true },
  )

  return { projects, repositories, permissions, loading, loaded, error, reload: load }
}
