// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listAllRepositories, listPackages, type PackageItem, type Repository } from '../api/repositories'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const PAGE_SIZE = 100

/** localStorage key of the repository last used on a security page. */
export const LAST_REPO_KEY = 'openartifact.security.lastRepo'

function readLastRepo(): string {
  try {
    return localStorage.getItem(LAST_REPO_KEY) ?? ''
  } catch {
    return ''
  }
}

function writeLastRepo(repo: string): void {
  try {
    localStorage.setItem(LAST_REPO_KEY, repo)
  } catch {
    // storage unavailable (private mode, quota): the selection just is not remembered
  }
}

export interface RepoPackagesOptions {
  /**
   * `auto` (default): pick the initial repository (valid `repo` URL query ->
   * last used repository -> first repository) and keep it in the URL query and
   * localStorage. `manual`: the page owns the selection and starts empty.
   */
  selection?: 'auto' | 'manual'
}

/**
 * Repository + package selection shared by the security pages. A package's
 * `id` is the `artifact_id` the scan endpoints expect.
 */
export function useRepoPackages(options: RepoPackagesOptions = {}) {
  const auto = (options.selection ?? 'auto') === 'auto'
  const toasts = useToastStore()
  const route = useRoute()
  const router = useRouter()
  const repos = ref<Repository[]>([])
  const reposLoaded = ref(false)
  const selectedRepo = ref('')
  const packages = ref<PackageItem[]>([])
  let seq = 0

  const known = (name: string): boolean => repos.value.some((r) => r.name === name)

  function queryRepo(): string {
    const q = route.query.repo
    return typeof q === 'string' ? q : ''
  }

  function initialRepo(): string {
    const fromQuery = queryRepo()
    if (fromQuery && known(fromQuery)) return fromQuery
    const last = readLastRepo()
    if (last && known(last)) return last
    return repos.value[0]?.name ?? ''
  }

  async function loadRepos(): Promise<void> {
    try {
      repos.value = await listAllRepositories()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sec.common.loadReposFailed'))
      return
    } finally {
      reposLoaded.value = true
    }
    if (auto && !selectedRepo.value) selectedRepo.value = initialRepo()
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

  if (auto) {
    // Selection -> URL (replace, so back does not step through every pick) and localStorage.
    watch(selectedRepo, (repo) => {
      if (repo) writeLastRepo(repo)
      if (!reposLoaded.value || queryRepo() === repo) return
      const { repo: _drop, ...rest } = route.query
      void router.replace({ query: repo ? { ...rest, repo } : rest })
    })
    // URL -> selection (back/forward or a manual edit); unknown repositories are ignored.
    watch(
      () => route.query.repo,
      () => {
        const wanted = queryRepo()
        if (wanted && known(wanted) && wanted !== selectedRepo.value) selectedRepo.value = wanted
      },
    )
  }

  onMounted(loadRepos)

  return { repos, reposLoaded, selectedRepo, packages, packageLabel, findPackage }
}
