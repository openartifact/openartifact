// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onBeforeUnmount, ref } from 'vue'
import { getVulnScan, type VulnScan, type VulnScanRow } from '../api/security'
import { listVexStatements, type VexRecord } from '../api/vex'
import { vexByVulnId } from '../lib/vex'
import { t } from '../i18n/en'

/**
 * Scan details (the full findings come from the scan's own repository) plus the
 * VEX decisions recorded for the open scan's artifact. `onVexChanged` runs after
 * a decision was saved or deleted from the details, so the page can reload the
 * repository's decision list.
 */
export function useVulnScanDetail(onVexChanged: () => void) {
  const detailRow = ref<VulnScanRow | null>(null)
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

  const vexByVuln = computed(() => vexByVulnId(vexRecords.value))

  async function loadVexRecords(row: VulnScanRow, seq: number): Promise<void> {
    try {
      const records = await listVexStatements(row.repository_name, row.artifact_id)
      if (seq !== detailSeq) return
      vexRecords.value = records
      vexError.value = ''
    } catch (err) {
      if (seq !== detailSeq) return
      vexError.value = err instanceof Error ? err.message : t('sec.vex.artifactLoadFailed')
    }
  }

  async function openDetail(row: VulnScanRow): Promise<void> {
    const seq = ++detailSeq
    detailRow.value = row
    detail.value = null
    detailError.value = ''
    detailLoading.value = true
    vexRecords.value = []
    vexError.value = ''
    vexVulnId.value = null
    // The decisions load beside the findings; a failure here must not hide the findings.
    void loadVexRecords(row, seq)
    try {
      const full = await getVulnScan(row.repository_name, row.id)
      if (seq !== detailSeq) return
      detail.value = full
    } catch (err) {
      if (seq !== detailSeq) return
      detailError.value = err instanceof Error ? err.message : t('sec.vuln.detailLoadFailed')
    } finally {
      if (seq === detailSeq) detailLoading.value = false
    }
  }

  function closeDetail(): void {
    detailSeq++
    detailRow.value = null
    detail.value = null
    detailLoading.value = false
    vexVulnId.value = null
  }

  /** The repository list deleted a decision: the open scan's badges must follow. */
  function reloadDetailVex(): void {
    if (detailRow.value) void loadVexRecords(detailRow.value, detailSeq)
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
    detailRow,
    detail,
    detailLoading,
    detailError,
    vexError,
    vexVulnId,
    vexByVuln,
    openDetail,
    closeDetail,
    reloadDetailVex,
    openVexDecision,
    closeVexDecision,
    onVexSaved,
    onVexDeleted,
  }
}
