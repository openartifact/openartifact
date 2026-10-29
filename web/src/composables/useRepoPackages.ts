// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, watch, onMounted } from 'vue'
import { listAllPackages, listAllRepositories, type PackageItem, type Repository } from '../api/repositories'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

/**
 * Repository list plus the packages of the repository the caller selected, for
 * the security forms. A package's `id` is the `artifact_id` the scan endpoints expect.
 */
export function useRepoPackages() {
  const toasts = useToastStore()
  const repos = ref<Repository[]>([])
  const reposLoaded = ref(false)
  const selectedRepo = ref('')
  const packages = ref<PackageItem[]>([])
  let seq = 0

  async function loadRepos(): Promise<void> {
    try {
      repos.value = await listAllRepositories()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('security.common.loadReposFailed'))
    } finally {
      reposLoaded.value = true
    }
  }

  async function loadPackages(): Promise<void> {
    const mySeq = ++seq
    const repo = selectedRepo.value
    // The previous repository's packages must not stay selectable while the new list loads.
    packages.value = []
    if (!repo) return
    try {
      const all = await listAllPackages(repo)
      if (mySeq === seq) packages.value = all
    } catch (err) {
      if (mySeq !== seq) return
      packages.value = []
      toasts.error(err instanceof Error ? err.message : t('security.common.loadPackagesFailed'))
    }
  }

  function findPackage(artifactId: string): PackageItem | undefined {
    return packages.value.find((p) => p.id === artifactId)
  }

  watch(selectedRepo, loadPackages)
  onMounted(loadRepos)

  return { repos, reposLoaded, selectedRepo, packages, findPackage }
}
