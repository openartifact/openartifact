// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref, shallowRef, watch, type ComputedRef, type Ref } from 'vue'
import { getVulnScanSummary } from '../api/security'

export interface BundleScanRow {
  repo: string
  total: number
  critical: number
  high: number
  /** The summary could not be loaded: the counts are unknown, not zero. */
  failed: boolean
}

export interface BundleScans {
  rows: Ref<BundleScanRow[]>
  loading: Ref<boolean>
  /** Critical findings across the repositories whose scans loaded. */
  critical: ComputedRef<number>
}

/** Vulnerability scan totals of the repositories a bundle's items come from, one request per repository. */
export function useBundleScans(repos: () => string[]): BundleScans {
  const rows = shallowRef<BundleScanRow[]>([])
  const loading = ref(false)
  let generation = 0

  async function load(names: string[]): Promise<void> {
    const mine = ++generation
    if (names.length === 0) {
      rows.value = []
      loading.value = false
      return
    }
    loading.value = true
    const next = await Promise.all(
      names.map(async (repo): Promise<BundleScanRow> => {
        try {
          const s = await getVulnScanSummary(repo)
          return { repo, total: s.total, critical: s.severity_at_least.critical ?? 0, high: s.severity_at_least.high ?? 0, failed: false }
        } catch {
          return { repo, total: 0, critical: 0, high: 0, failed: true }
        }
      }),
    )
    if (mine !== generation) return
    rows.value = next
    loading.value = false
  }

  // Keyed by the sorted, de-duplicated names so reloading an unchanged item list does not refetch.
  const key = computed(() => [...new Set(repos())].sort().join('\n'))
  watch(key, (k) => void load(k === '' ? [] : k.split('\n')), { immediate: true })

  return { rows, loading, critical: computed(() => rows.value.reduce((n, r) => n + r.critical, 0)) }
}
