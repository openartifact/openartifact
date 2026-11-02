// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onMounted, reactive, ref } from 'vue'
import {
  getExploitFeeds,
  getVulnGate,
  listFindings,
  setVulnQuarantine,
  type ExploitFeedsResponse,
  type SecurityFinding,
  type VulnGateResponse,
} from '../api/security'
import { epssInvalid, gateCriteria, unionFindings, type GateForm } from '../lib/scanGate'
import { useLoadable } from './useLoadable'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n'

const PAGE = 100

export interface Verdict {
  loading: boolean
  result?: VulnGateResponse
  error?: string
}

/**
 * The exploitation gate: which open vulnerability scans meet the criteria (found with the findings filters),
 * the gate verdict of one scan (the call a CI job makes), and quarantine, the only thing that blocks downloads.
 */
export function useScanGate() {
  const toasts = useToastStore()

  const feeds = useLoadable<ExploitFeedsResponse | null>(getExploitFeeds, null)
  const blocked = useLoadable<SecurityFinding[]>(async () => (await listFindings({ type: 'vuln', status: 'quarantined', sort: 'priority', limit: PAGE })).findings, [])

  const form = reactive<GateForm>({ kev: true, epssPercent: '' })
  const candidates = ref<SecurityFinding[]>([])
  const searched = ref(false)
  /** More scans meet the criteria than the list shows. */
  const truncated = ref(false)
  const searching = ref(false)
  const searchError = ref('')
  /** Gate verdicts by scan id. */
  const verdicts = ref<Record<string, Verdict>>({})
  /** The scan ids whose quarantine is being changed. */
  const changing = ref<Set<string>>(new Set())

  const criteria = computed(() => gateCriteria(form))
  const invalid = computed(() => epssInvalid(form))

  async function search(): Promise<void> {
    const c = criteria.value
    searchError.value = ''
    verdicts.value = {}
    if (!c || invalid.value) {
      candidates.value = []
      searched.value = false
      return
    }
    searching.value = true
    try {
      const base = { type: 'vuln', status: 'open', sort: 'priority', limit: PAGE } as const
      const lists = await Promise.all([
        c.kev ? listFindings({ ...base, kev: true }) : undefined,
        c.epss_min !== undefined ? listFindings({ ...base, epss_min: c.epss_min }) : undefined,
      ])
      const pages = lists.flatMap((l) => (l ? [l] : []))
      candidates.value = unionFindings(pages.map((p) => p.findings))
      truncated.value = pages.some((p) => p.has_more)
      searched.value = true
    } catch (err) {
      candidates.value = []
      searchError.value = err instanceof Error ? err.message : t('sx.gate.searchFailed')
    } finally {
      searching.value = false
    }
  }

  async function check(f: SecurityFinding): Promise<void> {
    const c = criteria.value
    if (!c) return
    verdicts.value = { ...verdicts.value, [f.id]: { loading: true } }
    try {
      const result = await getVulnGate(f.repository, f.id, c)
      verdicts.value = { ...verdicts.value, [f.id]: { loading: false, result } }
    } catch (err) {
      verdicts.value = { ...verdicts.value, [f.id]: { loading: false, error: err instanceof Error ? err.message : t('sx.gate.checkFailed') } }
    }
  }

  async function setBlocked(f: SecurityFinding, quarantined: boolean): Promise<boolean> {
    if (changing.value.has(f.id)) return false
    changing.value = new Set(changing.value).add(f.id)
    try {
      await setVulnQuarantine(f.repository, f.id, quarantined)
      const name = `${f.package_name}@${f.package_version}`
      toasts.success(tp(quarantined ? 'sx.gate.blocked' : 'sx.gate.released', { name }))
      // A scan moves between the two lists: the candidates lose it, the blocked list gains it, or the reverse.
      candidates.value = candidates.value.filter((x) => x.id !== f.id)
      await Promise.all([blocked.reload(), search()])
      return true
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sx.gate.changeFailed'))
      return false
    } finally {
      const next = new Set(changing.value)
      next.delete(f.id)
      changing.value = next
    }
  }

  onMounted(search)

  return { feeds, blocked, form, criteria, invalid, candidates, searched, truncated, searching, searchError, verdicts, changing, search, check, setBlocked }
}
