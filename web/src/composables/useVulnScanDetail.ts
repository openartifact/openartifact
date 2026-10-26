// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onBeforeUnmount, ref } from 'vue'
import { getVulnScan, type VulnScan } from '../api/security'
import { listVexStatements, type VexRecord } from '../api/vex'
import { vexByVulnId } from '../lib/vex'
import { t } from '../i18n'

/** The scan a finding stands for: its repository (every per-repository endpoint needs it) and id. */
export interface ScanRef {
  repository: string
  id: string
}

/**
 * Details of one vulnerability scan (the full findings come from the scan's own
 * repository) plus the VEX decisions recorded for its artifact. `onVexChanged`
 * runs after a decision was saved or deleted, so the page can refresh the list
 * and the repository's decision panel.
 */
export function useVulnScanDetail(onVexChanged: () => void) {
  const detail = ref<VulnScan | null>(null)
  const detailLoading = ref(false)
  const detailError = ref('')
  /** Decisions recorded for the open scan's artifact. */
  const vexRecords = ref<VexRecord[]>([])
  const vexError = ref('')
  /** The finding whose decision dialog is open. */
  const vexVulnId = ref<string | null>(null)
  // Bumped by every open/close; a response carrying an older value is discarded.
  let detailSeq = 0
  let current: ScanRef | null = null

  const vexByVuln = computed(() => vexByVulnId(vexRecords.value))

  async function loadVexRecords(repository: string, artifactId: string, seq: number): Promise<void> {
    try {
      const records = await listVexStatements(repository, artifactId)
      if (seq !== detailSeq) return
      vexRecords.value = records
      vexError.value = ''
    } catch (err) {
      if (seq !== detailSeq) return
      vexError.value = err instanceof Error ? err.message : t('security.vex.artifactLoadFailed')
    }
  }

  async function open(ref_: ScanRef): Promise<void> {
    const seq = ++detailSeq
    current = ref_
    detail.value = null
    detailError.value = ''
    detailLoading.value = true
    vexRecords.value = []
    vexError.value = ''
    vexVulnId.value = null
    try {
      const full = await getVulnScan(ref_.repository, ref_.id)
      if (seq !== detailSeq) return
      detail.value = full
      // The decisions load beside the findings; a failure here must not hide the findings.
      void loadVexRecords(ref_.repository, full.artifact_id, seq)
    } catch (err) {
      if (seq !== detailSeq) return
      detailError.value = err instanceof Error ? err.message : t('security.vuln.detailLoadFailed')
    } finally {
      if (seq === detailSeq) detailLoading.value = false
    }
  }

  function close(): void {
    detailSeq++
    current = null
    detail.value = null
    detailLoading.value = false
    vexVulnId.value = null
  }

  /** The scan open now, to re-read it after a change. */
  function reload(): Promise<void> {
    return current ? open(current) : Promise.resolve()
  }

  function openVexDecision(vulnId: string): void {
    vexVulnId.value = vulnId
  }

  function closeVexDecision(): void {
    vexVulnId.value = null
  }

  function onVexSaved(record: VexRecord): void {
    vexRecords.value = [...vexRecords.value.filter((r) => r.vuln_id !== record.vuln_id), record]
    vexVulnId.value = null
    onVexChanged()
  }

  function onVexDeleted(id: string): void {
    vexRecords.value = vexRecords.value.filter((r) => r.id !== id)
    vexVulnId.value = null
    onVexChanged()
  }

  onBeforeUnmount(() => {
    detailSeq++
  })

  return {
    detail,
    detailLoading,
    detailError,
    vexError,
    vexVulnId,
    vexByVuln,
    open,
    close,
    reload,
    openVexDecision,
    closeVexDecision,
    onVexSaved,
    onVexDeleted,
  }
}
