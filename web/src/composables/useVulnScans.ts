// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQuery } from 'vue-router'
import {
  deleteVulnScan,
  getVulnScanSummary,
  listAllVulnScans,
  setVulnQuarantine,
  vulnScansExportUrl,
  type ScanExportFormat,
  type VulnScanRow,
  type VulnScanSummary,
} from '../api/security'
import {
  EMPTY_VULN_FILTERS,
  buildVulnQuery,
  hasVulnFilters,
  parseVulnFilters,
  toVulnScanFilters,
  type VulnFilterState,
} from '../lib/vulnScanFilters'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const POLL_MS = 3000
export const VULN_PAGE_LIMIT = 25

/**
 * The cross-repository vulnerability scan overview: URL-driven filters, the
 * summary (which follows the repository filter only), the keyset-paginated
 * scan list with polling while a scan runs, and the quarantine / delete row
 * actions. `onDeleted` runs after a scan was deleted.
 */
export function useVulnScans(onDeleted: (scan: VulnScanRow) => void = () => {}) {
  const toasts = useToastStore()
  const route = useRoute()
  const router = useRouter()

  // ─── Filters: the URL query is the single source of truth ─────────────────

  const filters = computed(() => parseVulnFilters(route.query))
  const filtersKey = computed(() => JSON.stringify(filters.value))
  const hasFilters = computed(() => hasVulnFilters(filters.value))

  /** Export of the scans the active filters select (the whole selection, not the loaded pages). */
  function exportUrl(format: ScanExportFormat): string {
    return vulnScansExportUrl(toVulnScanFilters(filters.value), format)
  }

  // The query being navigated to: two changes before the first navigation settles must both apply.
  let pendingQuery: LocationQuery | undefined

  function setFilter(patch: Partial<VulnFilterState>): void {
    const current = pendingQuery ?? route.query
    const query = buildVulnQuery(current, { ...parseVulnFilters(current), ...patch })
    pendingQuery = query as LocationQuery
    void router.replace({ query }).finally(() => {
      if (pendingQuery === query) pendingQuery = undefined
    })
  }

  function clearFilters(): void {
    setFilter(EMPTY_VULN_FILTERS)
  }

  // ─── Summary (respects the repository filter only) ────────────────────────

  const summary = ref<VulnScanSummary | null>(null)
  const summaryError = ref('')
  let summarySeq = 0

  async function loadSummary(): Promise<void> {
    const seq = ++summarySeq
    try {
      const result = await getVulnScanSummary(filters.value.repo || undefined)
      if (seq !== summarySeq) return
      summary.value = result
      summaryError.value = ''
    } catch (err) {
      if (seq !== summarySeq) return
      summary.value = null
      summaryError.value = err instanceof Error ? err.message : t('sec.vuln.summaryFailed')
    }
  }

  // ─── Scan list (keyset-paginated, worst first as returned) ────────────────

  const scans = ref<VulnScanRow[]>([])
  const hasMore = ref(false)
  const nextCursor = ref('')
  const pagesLoaded = ref(0)
  // True from the start: the filters watcher below loads the first page immediately.
  const loading = ref(true)
  const loadingMore = ref(false)
  const loadError = ref('')

  let pollTimer: ReturnType<typeof setTimeout> | undefined
  // Bumped by every list (re)load; a response carrying an older value is discarded.
  let listSeq = 0

  function schedulePoll(): void {
    clearTimeout(pollTimer)
    if (scans.value.some((s) => s.scan_status === 'scanning')) {
      pollTimer = setTimeout(() => void refresh(true), POLL_MS)
    }
  }

  // `silent` refreshes (polling) keep the table visible instead of flashing the spinner.
  async function loadScans(silent = false): Promise<void> {
    clearTimeout(pollTimer)
    const seq = ++listSeq
    loadingMore.value = false
    if (!silent) {
      loading.value = true
      loadError.value = ''
      scans.value = []
      hasMore.value = false
      nextCursor.value = ''
      pagesLoaded.value = 0
    }
    try {
      const page = await listAllVulnScans({ ...toVulnScanFilters(filters.value), limit: VULN_PAGE_LIMIT })
      if (seq !== listSeq) return
      if (silent && pagesLoaded.value > 1) {
        // Pages beyond the first stay loaded: refresh the rows in place.
        const fresh = new Map(page.scans.map((s) => [s.id, s]))
        scans.value = scans.value.map((s) => fresh.get(s.id) ?? s)
      } else {
        scans.value = page.scans
        hasMore.value = page.has_more
        nextCursor.value = page.next_cursor ?? ''
        pagesLoaded.value = 1
      }
      loadError.value = ''
    } catch (err) {
      if (seq !== listSeq) return
      if (!silent) loadError.value = err instanceof Error ? err.message : t('sec.vuln.loadFailed')
    } finally {
      if (seq === listSeq) {
        loading.value = false
        schedulePoll()
      }
    }
  }

  async function loadMore(): Promise<void> {
    if (!hasMore.value || !nextCursor.value || loadingMore.value || loading.value) return
    const seq = listSeq
    loadingMore.value = true
    try {
      const page = await listAllVulnScans({
        ...toVulnScanFilters(filters.value),
        limit: VULN_PAGE_LIMIT,
        cursor: nextCursor.value,
      })
      if (seq !== listSeq) return
      const seen = new Set(scans.value.map((s) => s.id))
      scans.value = [...scans.value, ...page.scans.filter((s) => !seen.has(s.id))]
      hasMore.value = page.has_more
      nextCursor.value = page.next_cursor ?? ''
      pagesLoaded.value++
    } catch (err) {
      if (seq !== listSeq) return
      toasts.error(err instanceof Error ? err.message : t('sec.vuln.loadMoreFailed'))
    } finally {
      if (seq === listSeq) {
        loadingMore.value = false
        schedulePoll()
      }
    }
  }

  function refresh(silent = false): Promise<unknown> {
    return Promise.all([loadScans(silent), loadSummary()])
  }

  // Any filter change starts over with the first page; the summary only follows the repository.
  watch(filtersKey, () => void loadScans(), { immediate: true })
  watch(
    () => filters.value.repo,
    () => {
      summary.value = null
      void loadSummary()
    },
    { immediate: true },
  )

  /** `name@version` per scanned artifact the loaded rows know, for the decisions list. */
  const artifactLabels = computed<Record<string, string>>(() =>
    Object.fromEntries(scans.value.map((s) => [s.artifact_id, `${s.package_name}@${s.package_version}`])),
  )

  // ─── Row actions ──────────────────────────────────────────────────────────

  const quarantining = ref<string | null>(null)

  async function toggleQuarantine(scan: VulnScanRow): Promise<void> {
    if (quarantining.value) return
    quarantining.value = scan.id
    try {
      const updated = await setVulnQuarantine(scan.repository_name, scan.id, !scan.quarantined)
      const wanted = filters.value.quarantined
      if (wanted && String(updated.quarantined) !== wanted) {
        // The row no longer matches the quarantine filter.
        scans.value = scans.value.filter((s) => s.id !== scan.id)
      } else {
        scans.value = scans.value.map((s) => (s.id === scan.id ? { ...s, quarantined: updated.quarantined } : s))
      }
      toasts.success(updated.quarantined ? t('sec.vuln.quarantined') : t('sec.vuln.quarantineRemoved'))
      void loadSummary()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sec.vuln.quarantineFailed'))
    } finally {
      quarantining.value = null
    }
  }

  const deleteTarget = ref<VulnScanRow | null>(null)
  const deleting = ref(false)

  async function confirmDelete(): Promise<void> {
    const target = deleteTarget.value
    if (!target || deleting.value) return
    deleting.value = true
    try {
      await deleteVulnScan(target.repository_name, target.id)
      scans.value = scans.value.filter((s) => s.id !== target.id)
      onDeleted(target)
      deleteTarget.value = null
      toasts.success(t('sec.vuln.deleted'))
      void loadSummary()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sec.vuln.deleteFailed'))
    } finally {
      deleting.value = false
    }
  }

  onBeforeUnmount(() => {
    clearTimeout(pollTimer)
    listSeq++
    summarySeq++
  })

  return {
    filters,
    hasFilters,
    exportUrl,
    setFilter,
    clearFilters,
    summary,
    summaryError,
    scans,
    hasMore,
    loading,
    loadingMore,
    loadError,
    artifactLabels,
    refresh,
    loadMore,
    quarantining,
    toggleQuarantine,
    deleteTarget,
    deleting,
    confirmDelete,
  }
}
