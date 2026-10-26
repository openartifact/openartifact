// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onMounted, ref, shallowRef } from 'vue'
import { getAdminSystemInfo, type AdminSystemInfo } from '../api/admin'
import { listAudit } from '../api/audit'
import {
  countTokens,
  countUsers,
  getFindingsSummary,
  getSecurityHealth,
  listRecentPackages,
  listRepositoryPage,
  listTokens,
  type FindingsSummary,
  type RecentPackage,
  type RepositoryPage,
  type SecurityHealth,
  type TokenPage,
} from '../api/dashboard'
import { listProjectActivity, listProjects } from '../api/projects'
import { listStorageBackends, type StorageBackend } from '../api/storage'
import {
  auditItem,
  isFeedEntry,
  mergeActivity,
  projectItem,
  publishedItem,
  type ActivityItem,
  type ResolvedAuditEntry,
} from '../lib/activity'
import { deriveAttention, tokenHorizon } from '../lib/attention'
import { dataOf, loadingBlock, toBlock, type Block } from '../lib/block'
import { deriveChecklist, isFreshInstance, progress } from '../lib/checklist'
import { deriveHealth } from '../lib/health'
import { useSessionStore } from '../stores/session'

const DISMISS_KEY = 'oa.dashboard.checklist.dismissed'
const FEED_LIMIT = 8
/** Projects whose feed a developer sees on Home. */
const FEED_PROJECTS = 4

function readDismissed(): boolean {
  try {
    return localStorage.getItem(DISMISS_KEY) === '1'
  } catch {
    return false
  }
}

/**
 * Everything the Home page shows. Each source loads on its own and lands in a block, so a
 * slow or forbidden endpoint never blanks the rest of the page.
 */
export function useDashboard() {
  const session = useSessionStore()
  const isAdmin = computed(() => session.isAdmin)

  const system = shallowRef<Block<AdminSystemInfo>>(loadingBlock())
  const storage = shallowRef<Block<StorageBackend[]>>(loadingBlock())
  const security = shallowRef<Block<SecurityHealth>>(loadingBlock())
  const summary = shallowRef<Block<FindingsSummary>>(loadingBlock())
  const nearQuota = shallowRef<Block<RepositoryPage>>(loadingBlock())
  const flagged = shallowRef<Block<RepositoryPage>>(loadingBlock())
  const unscanned = shallowRef<Block<RepositoryPage>>(loadingBlock())
  const tokens = shallowRef<Block<TokenPage>>(loadingBlock())
  const repos = shallowRef<Block<RepositoryPage>>(loadingBlock())
  const recent = shallowRef<Block<RecentPackage[]>>(loadingBlock())
  const mine = shallowRef<Block<RecentPackage[]>>(loadingBlock())
  const activity = shallowRef<Block<ActivityItem[]>>(loadingBlock())
  const projectCount = shallowRef<Block<number>>(loadingBlock())
  const userCount = shallowRef<Block<number>>(loadingBlock())
  const tokenCount = shallowRef<Block<number>>(loadingBlock())
  const now = ref(Date.now())
  const dismissed = ref(readDismissed())

  const health = computed(() =>
    deriveHealth({
      system: dataOf(system.value),
      storage: dataOf(storage.value),
      security: dataOf(security.value),
      nearQuota: dataOf(nearQuota.value)?.total,
    }),
  )

  const attention = computed(() =>
    deriveAttention({
      now: now.value,
      health: dataOf(security.value),
      summary: dataOf(summary.value),
      storage: dataOf(storage.value),
      nearQuota: dataOf(nearQuota.value)?.repositories,
      flagged: dataOf(flagged.value),
      unscanned: dataOf(unscanned.value),
      tokens: dataOf(tokens.value)?.tokens,
    }),
  )

  const attentionSources = [summary, nearQuota, flagged, tokens, security, storage, unscanned] as { value: Block<unknown> }[]
  const attentionLoading = computed(() => attentionSources.some((b) => b.value.state === 'loading'))
  /** Sources that failed (as opposed to being off limits): the list may be incomplete. */
  const attentionFailed = computed(() => attentionSources.filter((b) => b.value.state === 'error').length)

  const counts = computed(() => ({
    repositories: dataOf(repos.value)?.total,
    packages: dataOf(recent.value)?.length,
    tokens: dataOf(tokenCount.value),
    scannerReady: dataOf(security.value)?.scanners.some((s) => s.ready),
    users: dataOf(userCount.value),
  }))
  const steps = computed(() => deriveChecklist(isAdmin.value, counts.value))
  const checklist = computed(() => ({ steps: steps.value, ...progress(steps.value) }))
  const fresh = computed(() => isFreshInstance(counts.value))
  /** The checklist shows on a fresh instance until it is complete or dismissed. */
  const showChecklist = computed(
    () => fresh.value && !dismissed.value && steps.value.length > 0 && checklist.value.done < checklist.value.total,
  )

  const totals = computed(() => {
    const page = dataOf(repos.value)
    const list = page?.repositories ?? []
    // The format count is exact only when the loaded page holds every repository.
    const formats = page && list.length >= page.total ? new Set(list.map((r) => r.format)).size : undefined
    return {
      repositories: page?.total,
      formats,
      projects: dataOf(projectCount.value),
      users: dataOf(userCount.value),
    }
  })

  function dismissChecklist(): void {
    dismissed.value = true
    try {
      localStorage.setItem(DISMISS_KEY, '1')
    } catch {
      // Storage is blocked: the dismissal lasts for this page view only.
    }
  }

  async function loadActivity(): Promise<Block<ActivityItem[]>> {
    if (isAdmin.value) {
      return toBlock(async () => {
        // Requests, downloads, routine sign-ins and entries without a subject are not activity.
        const res = await listAudit({ limit: 100 })
        return mergeActivity([res.items.map((e) => e as ResolvedAuditEntry).filter(isFeedEntry).map(auditItem)], FEED_LIMIT)
      })
    }
    return toBlock(async () => {
      const names = session.projects.slice(0, FEED_PROJECTS).map((p) => p.name)
      const feeds = await Promise.all(
        names.map(async (name) => (await listProjectActivity(name, { limit: FEED_LIMIT })).items.map((e) => projectItem(e, name))),
      )
      const published = dataOf(mine.value)?.map(publishedItem) ?? []
      return mergeActivity([...feeds, published], FEED_LIMIT)
    })
  }

  async function load(): Promise<void> {
    now.value = Date.now()
    const admin = isAdmin.value
    const settle = <T>(target: { value: Block<T> }, loader: () => Promise<T>): Promise<void> =>
      toBlock(loader).then((b) => void (target.value = b))
    const skip = <T>(target: { value: Block<T> }): void => void (target.value = { state: 'forbidden' })

    const tasks: Promise<void>[] = [
      settle(repos, () => listRepositoryPage({ sort: 'updated', limit: 100 })),
      settle(nearQuota, () => listRepositoryPage({ near_quota: true })),
      settle(flagged, () => listRepositoryPage({ flagged: true, limit: 1 })),
      settle(tokens, () => listTokens({ expiresBefore: tokenHorizon(now.value) })),
      settle(recent, () => listRecentPackages({ mine: false, limit: FEED_LIMIT })),
      settle(projectCount, async () => (await listProjects()).projects.length),
      settle(summary, getFindingsSummary),
    ]
    if (admin) {
      tasks.push(
        settle(system, getAdminSystemInfo),
        settle(storage, async () => (await listStorageBackends()).items),
        settle(security, getSecurityHealth),
        settle(unscanned, () => listRepositoryPage({ unscanned: true, limit: 1 })),
        settle(userCount, countUsers),
        settle(tokenCount, countTokens),
      )
    } else {
      for (const b of [system, storage, security, unscanned, userCount] as { value: Block<unknown> }[]) skip(b)
      tasks.push(
        settle(tokenCount, countTokens),
        settle(mine, () => listRecentPackages({ mine: true, limit: FEED_LIMIT })),
      )
    }
    await Promise.all(tasks)
    activity.value = await loadActivity()
  }

  onMounted(() => void load())

  return {
    isAdmin,
    system,
    security,
    summary,
    nearQuota,
    tokens,
    repos,
    recent,
    mine,
    activity,
    health,
    attention,
    attentionLoading,
    attentionFailed,
    checklist,
    fresh,
    showChecklist,
    totals,
    dismissChecklist,
    reload: load,
  }
}
