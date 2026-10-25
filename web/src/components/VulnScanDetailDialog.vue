<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="row !== null"
    drawer
    max-width="max-w-3xl"
    :title="t('sec.vuln.detailTitle')"
    :description="row ? `${row.repository_name} · ${row.package_name}@${row.package_version}` : ''"
    @close="emit('close')"
  >
    <div v-if="loading" class="py-8 flex justify-center text-muted" role="status">
      <ArrowPathIcon class="w-5 h-5 animate-spin" aria-hidden="true" />
      <span class="sr-only">{{ t('sec.common.loadingScans') }}</span>
    </div>
    <p v-else-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <div v-else-if="detail" class="space-y-4" data-testid="scan-detail">
      <div class="flex items-start justify-between gap-3 flex-wrap">
        <VulnScanStatus :status="detail.scan_status" :scan-error="detail.scan_error" :failed-scanners="detail.failed_scanners" />
        <VexDownloadButtons
          v-if="row"
          :repo="row.repository_name"
          :artifact-id="detail.artifact_id"
          size="sm"
          test-id-prefix="vex-artifact-download"
        />
      </div>
      <p v-if="vexError" class="text-sm text-danger" role="alert" data-testid="vex-load-error">{{ vexError }}</p>
      <h3 class="text-sm font-semibold">{{ t('sec.vuln.findings') }}</h3>
      <VulnScanFindingsTable :scan="detail" :vex-by-vuln="vexByVuln" @decide="(id) => emit('decide', id)" />
    </div>
    <!-- Nested inside the drawer: HeadlessUI closes a dialog on clicks outside its own tree. -->
    <VexDecisionDialog
      v-if="row && detail"
      :open="vexVulnId !== null"
      :repo="row.repository_name"
      :artifact-id="detail.artifact_id"
      :artifact-label="`${detail.package_name}@${detail.package_version}`"
      :vuln-id="vexVulnId ?? ''"
      :existing="vexVulnId !== null ? (vexByVuln.get(vexVulnId) ?? null) : null"
      @close="emit('close-decision')"
      @saved="(record) => emit('saved', record)"
      @deleted="(id) => emit('deleted', id)"
    />
  </AdminDialog>
</template>

<script setup lang="ts">
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import VexDecisionDialog from './VexDecisionDialog.vue'
import VexDownloadButtons from './VexDownloadButtons.vue'
import VulnScanFindingsTable from './VulnScanFindingsTable.vue'
import VulnScanStatus from './VulnScanStatus.vue'
import type { VulnScan, VulnScanRow } from '../api/security'
import type { VexRecord } from '../api/vex'
import { t } from '../i18n/en'

defineProps<{
  /** The scan the details were opened for; null while the dialog is closed. */
  row: VulnScanRow | null
  detail: VulnScan | null
  loading: boolean
  error: string
  vexError: string
  vexByVuln: Map<string, VexRecord>
  /** The finding whose decision dialog is open. */
  vexVulnId: string | null
}>()

const emit = defineEmits<{
  close: []
  decide: [vulnId: string]
  'close-decision': []
  saved: [record: VexRecord]
  deleted: [id: string]
}>()
</script>
