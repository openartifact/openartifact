<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <div v-if="detailLoading" class="flex justify-center py-8 text-muted" role="status">
      <ArrowPathIcon class="h-5 w-5 animate-spin" aria-hidden="true" />
      <span class="sr-only">{{ t('security.detail.loading') }}</span>
    </div>
    <p v-else-if="detailError" class="text-sm text-danger" role="alert">{{ detailError }}</p>
    <div v-else-if="detail" class="space-y-4" data-testid="scan-detail">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <VulnScanStatus :status="detail.scan_status" :scan-error="detail.scan_error" :failed-scanners="detail.failed_scanners" />
        <div class="flex flex-wrap items-center gap-2">
          <ScanExportButtons
            :formats="SCAN_EXPORT_FORMATS"
            :url="(format) => vulnScanExportUrl(finding.repository, finding.id, format as ScanExportFormat)"
            :fallback-name="(format) => `${finding.package_name}-${finding.package_version}.${format}`"
            size="sm"
          />
          <VexDownloadButtons
            :repo="finding.repository"
            :artifact-id="detail.artifact_id"
            size="sm"
            test-id-prefix="vex-artifact-download"
          />
        </div>
      </div>
      <p v-if="vexError" class="text-sm text-danger" role="alert" data-testid="vex-load-error">{{ vexError }}</p>
      <h3 class="text-sm font-semibold">{{ t('security.vuln.findings') }}</h3>
      <VulnScanFindingsTable :scan="detail" :vex-by-vuln="vexByVuln" :can-decide="canAct" @decide="openVexDecision" />
    </div>
    <!-- Nested inside the drawer: HeadlessUI closes a dialog on clicks outside its own tree. -->
    <VexDecisionDialog
      v-if="detail"
      :open="vexVulnId !== null"
      :repo="finding.repository"
      :artifact-id="detail.artifact_id"
      :artifact-label="`${detail.package_name}@${detail.package_version}`"
      :vuln-id="vexVulnId ?? ''"
      :existing="vexVulnId !== null ? (vexByVuln.get(vexVulnId) ?? null) : null"
      @close="closeVexDecision"
      @saved="onVexSaved"
      @deleted="onVexDeleted"
    />
  </div>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import ScanExportButtons from '../ScanExportButtons.vue'
import VexDecisionDialog from '../VexDecisionDialog.vue'
import VexDownloadButtons from '../VexDownloadButtons.vue'
import VulnScanFindingsTable from '../VulnScanFindingsTable.vue'
import VulnScanStatus from '../VulnScanStatus.vue'
import { SCAN_EXPORT_FORMATS, vulnScanExportUrl, type ScanExportFormat, type SecurityFinding } from '../../api/security'
import { useVulnScanDetail } from '../../composables/useVulnScanDetail'
import { t } from '../../i18n/en'

const props = defineProps<{
  finding: SecurityFinding
  /** The caller may record VEX decisions. */
  canAct: boolean
}>()

const emit = defineEmits<{ changed: [] }>()

const {
  detail,
  detailLoading,
  detailError,
  vexError,
  vexVulnId,
  vexByVuln,
  open,
  openVexDecision,
  closeVexDecision,
  onVexSaved,
  onVexDeleted,
} = useVulnScanDetail(() => emit('changed'))

watch(() => props.finding.id, () => void open({ repository: props.finding.repository, id: props.finding.id }), { immediate: true })
</script>
