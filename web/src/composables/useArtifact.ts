// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, inject, provide, ref, shallowRef, watch, type ComputedRef, type InjectionKey, type Ref } from 'vue'
import { getPackageDetail, getSecuritySummary, type PackageDetail, type SecuritySummary } from '../api/artifact'
import { getRepository, listPackageVersions, type PackageItem, type Repository } from '../api/repositories'
import { artifactCapabilities, type ArtifactCapabilities } from '../lib/artifactSecurity'
import { groupVersions, primaryFile, selectVersion, type VersionEntry } from '../lib/artifactVersions'
import { useSessionStore } from '../stores/session'

/** What every part of the artifact page reads; provided by the page, injected by its tabs. */
export interface ArtifactContext {
  repoName: ComputedRef<string>
  pkgName: ComputedRef<string>
  repository: Ref<Repository | null>
  format: ComputedRef<string>
  versions: ComputedRef<VersionEntry[]>
  /** The version the page shows (the requested one, else the latest). */
  entry: ComputedRef<VersionEntry | null>
  detail: Ref<PackageDetail | null>
  /** Why the shown version's detail could not load; empty otherwise. */
  detailError: Ref<string>
  /** Counts of the shown version's scans; null while loading, when it failed or for a never-scanned version. */
  summary: Ref<SecuritySummary | null>
  caps: ComputedRef<ArtifactCapabilities>
  /** Reloads the version list and everything of the shown version. */
  reload: () => Promise<void>
  /** Reloads the shown version's security counts (after a quarantine, VEX decision or scan). */
  reloadSecurity: () => Promise<void>
}

const KEY: InjectionKey<ArtifactContext> = Symbol('artifact')

export function useArtifactContext(): ArtifactContext {
  const ctx = inject(KEY)
  if (!ctx) throw new Error('useArtifactContext outside the artifact page')
  return ctx
}

interface ArtifactSource {
  repo: () => string
  pkg: () => string
  /** The version of the URL; undefined for the package URL (latest). */
  version: () => string | undefined
}

/**
 * Loads one package of one repository: the repository (format, permissions), the rows of the
 * package grouped into versions, and, for the shown version, its detail and security counts.
 * A response that arrives after the route moved on is dropped.
 */
export function useArtifact(source: ArtifactSource) {
  const session = useSessionStore()
  const repository = ref<Repository | null>(null)
  const rows = shallowRef<PackageItem[]>([])
  const loading = ref(true)
  const error = ref('')
  const detail = ref<PackageDetail | null>(null)
  const detailError = ref('')
  const summary = ref<SecuritySummary | null>(null)

  const repoName = computed(source.repo)
  const pkgName = computed(source.pkg)
  const format = computed(() => repository.value?.format ?? rows.value[0]?.format ?? '')
  const versions = computed(() => groupVersions(rows.value))
  const entry = computed(() => selectVersion(versions.value, source.version()))
  const caps = computed(() => artifactCapabilities({ isAdmin: session.isAdmin, permissions: repository.value?.permissions }))

  let listGeneration = 0
  async function loadList(): Promise<void> {
    const mine = ++listGeneration
    loading.value = true
    error.value = ''
    try {
      const [repo, list] = await Promise.all([getRepository(repoName.value), listPackageVersions(repoName.value, pkgName.value)])
      if (mine !== listGeneration) return
      repository.value = repo
      rows.value = list.filter((p) => p.name === pkgName.value)
    } catch (err) {
      if (mine !== listGeneration) return
      rows.value = []
      error.value = err instanceof Error ? err.message : String(err)
    } finally {
      if (mine === listGeneration) loading.value = false
    }
  }

  let detailGeneration = 0
  async function loadDetail(): Promise<void> {
    const mine = ++detailGeneration
    const current = entry.value
    const file = current ? primaryFile(current) : undefined
    detail.value = null
    detailError.value = ''
    if (!file) return
    try {
      const res = await getPackageDetail(repoName.value, file.id)
      if (mine === detailGeneration) detail.value = res
    } catch (err) {
      if (mine === detailGeneration) detailError.value = err instanceof Error ? err.message : String(err)
    }
  }

  let summaryGeneration = 0
  async function loadSummary(): Promise<void> {
    const mine = ++summaryGeneration
    const current = entry.value
    if (!current) {
      summary.value = null
      return
    }
    try {
      const res = await getSecuritySummary(repoName.value, pkgName.value, current.version)
      if (mine === summaryGeneration) summary.value = res
    } catch {
      // Counts are an extra: without them the page shows "not scanned" instead of failing.
      if (mine === summaryGeneration) summary.value = null
    }
  }

  watch(
    [repoName, pkgName],
    () => {
      // Another package: nothing of the previous one may show while this one loads.
      rows.value = []
      repository.value = null
      void loadList()
    },
    { immediate: true },
  )
  // The shown version changes with the URL and when the list (re)loads.
  watch(
    () => (entry.value ? `${entry.value.version}\u0000${primaryFile(entry.value)?.id ?? ''}` : ''),
    () => {
      summary.value = null
      void loadDetail()
      void loadSummary()
    },
    { immediate: true },
  )

  const context: ArtifactContext = {
    repoName,
    pkgName,
    repository,
    format,
    versions,
    entry,
    detail,
    detailError,
    summary,
    caps,
    reload: async () => {
      await loadList()
      await Promise.all([loadDetail(), loadSummary()])
    },
    reloadSecurity: loadSummary,
  }
  provide(KEY, context)

  return { ...context, loading, error }
}
