// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ApiError } from '../api/client'
import {
  getFindingsSummary,
  getSecurityHealth,
  listFindings,
  listVulnScans,
  type SecurityHealth,
  type SecuritySummary,
} from '../api/security'
import {
  INBOX_FILTERS,
  hasNarrowingFilters,
  stripPatch,
  toFindingFilters,
  toFindingRow,
  type InboxFilters,
  type SeverityFilter,
} from '../lib/securityFindings'
import { useSessionStore } from '../stores/session'
import { useListQuery } from './useListQuery'
import { t } from '../i18n/en'

export const INBOX_PAGE_LIMIT = 25

/**
 * The security inbox: URL-bound filters and the cursor-paged findings list, the
 * summary strip (which follows the repository filter only and sets filters when
 * clicked), the scanner health (administrators), and the all-clear / forbidden
 * decisions that tell an empty scope from an empty filter result.
 */
export function useSecurityInbox() {
  const session = useSessionStore()
  const forbidden = ref(false)

  const list = useListQuery({
    filters: INBOX_FILTERS,
    paging: 'cursor',
    limit: INBOX_PAGE_LIMIT,
    debounce: ['q'],
    fetch: async (req) => {
      try {
        const page = await listFindings({ ...toFindingFilters(req.filters as InboxFilters), limit: req.limit, cursor: req.cursor })
        forbidden.value = false
        return { rows: page.findings.map(toFindingRow), hasMore: page.has_more, nextCursor: page.next_cursor }
      } catch (err) {
        // A caller who maintains no project has no findings scope: the server says 403.
        if (err instanceof ApiError && err.status === 403) forbidden.value = true
        throw err
      }
    },
  })

  const filters = computed(() => list.filters.value as InboxFilters)

  // ─── Summary: follows the repository filter only ──────────────────────────

  const summary = ref<SecuritySummary | null>(null)
  const summaryError = ref('')
  let summarySeq = 0

  async function loadSummary(): Promise<void> {
    const seq = ++summarySeq
    try {
      const result = await getFindingsSummary(filters.value.repo || undefined)
      if (seq !== summarySeq) return
      summary.value = result
      summaryError.value = ''
    } catch (err) {
      if (seq !== summarySeq) return
      summary.value = null
      summaryError.value = err instanceof Error ? err.message : t('security.summary.failed')
    }
  }

  watch(() => filters.value.repo, () => void loadSummary(), { immediate: true })

  // ─── Health (administrators): scanner readiness, backlog, AI switch ───────

  const health = ref<SecurityHealth | null>(null)
  async function loadHealth(): Promise<void> {
    if (!session.isAdmin) return
    try {
      health.value = await getSecurityHealth()
    } catch {
      // Health is advice only: the inbox works without it.
      health.value = null
    }
  }
  void loadHealth()

  // ─── Names of the artifacts the repository's VEX decisions refer to ───────

  const vexLabels = ref<Record<string, string>>({})
  let labelsSeq = 0
  watch(
    () => [filters.value.repo, session.isAdmin] as const,
    async ([repo, admin]) => {
      const seq = ++labelsSeq
      vexLabels.value = {}
      if (!repo || !admin) return
      try {
        const scans = await listVulnScans(repo)
        if (seq === labelsSeq) {
          vexLabels.value = Object.fromEntries(scans.map((s) => [s.artifact_id, `${s.package_name}@${s.package_version}`]))
        }
      } catch {
        // The panel falls back to the artifact id it has.
      }
    },
    { immediate: true },
  )

  // ─── Derived states ───────────────────────────────────────────────────────

  /** Scope has nothing to triage: the list is empty, no filter narrows it, and the summary agrees. */
  const allClear = computed(() => {
    if (list.loading.value || list.error.value || list.rows.value.length > 0) return false
    if (hasNarrowingFilters(filters.value)) return false
    const s = summary.value
    if (!s) return false
    return s.scanned_repositories > 0 && (filters.value.type === 'all' ? Object.values(s.by_type) : [s.by_type[filters.value.type]]).every((x) => x.total === 0)
  })

  /** Nothing was ever scanned in this scope: teach how to scan instead of declaring all clear. */
  const neverScanned = computed(() => {
    const s = summary.value
    return !!s && s.scanned_repositories === 0 && list.rows.value.length === 0 && !list.loading.value && !list.error.value && !hasNarrowingFilters(filters.value)
  })

  /** Selecting a strip cell narrows the list; clicking the active one releases it. */
  function applyStrip(cell: SeverityFilter | 'quarantined'): void {
    list.setFilters(stripPatch(filters.value, cell))
  }

  /** The list and the summary after a change made from the drawer. */
  function refresh(): Promise<unknown> {
    return Promise.all([list.reload(), loadSummary(), loadHealth()])
  }

  onBeforeUnmount(() => {
    summarySeq++
    labelsSeq++
  })

  return {
    list,
    filters,
    forbidden,
    summary,
    summaryError,
    health,
    vexLabels,
    allClear,
    neverScanned,
    applyStrip,
    refresh,
  }
}
