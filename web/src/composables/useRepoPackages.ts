// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, watch, onMounted } from 'vue'
import { listAllRepositories, listPackages, type PackageItem, type Repository } from '../api/repositories'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const PAGE_SIZE = 100

/**
 * Repository + package selection shared by the security pages. A package's
 * `id` is the `artifact_id` the scan endpoints expect.
 */
export function useRepoPackages() {
  const toasts = useToastStore()
  const repos = ref<Repository[]>([])
  const selectedRepo = ref('')
  const packages = ref<PackageItem[]>([])
  let seq = 0

  async function loadRepos(): Promise<void> {
    try {
      repos.value = await listAllRepositories()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sec.common.loadReposFailed'))
    }
  }

  async function loadPackages(): Promise<void> {
    const mySeq = ++seq
    const repo = selectedRepo.value
    // The previous repository's packages must not stay selectable while the new list loads.
    packages.value = []
    if (!repo) return
    try {
      const all: PackageItem[] = []
      for (let page = 1; ; page++) {
        const res = await listPackages(repo, { page, limit: PAGE_SIZE })
        all.push(...(res.packages ?? []))
        if (all.length >= res.total || (res.packages ?? []).length < PAGE_SIZE) break
      }
      if (mySeq === seq) packages.value = all
    } catch (err) {
      if (mySeq !== seq) return
      packages.value = []
      toasts.error(err instanceof Error ? err.message : t('sec.common.loadPackagesFailed'))
    }
  }

  /** `name@version` for a known artifact id, else the id itself. */
  function packageLabel(artifactId: string): string {
    const pkg = packages.value.find((p) => p.id === artifactId)
    return pkg ? `${pkg.name}@${pkg.version}` : artifactId
  }

  function findPackage(artifactId: string): PackageItem | undefined {
    return packages.value.find((p) => p.id === artifactId)
  }

  watch(selectedRepo, loadPackages)
  onMounted(loadRepos)

  return { repos, selectedRepo, packages, packageLabel, findPackage }
}
