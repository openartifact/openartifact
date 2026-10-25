<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <tr class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0">
    <td class="px-4 py-3">
      <div class="flex items-center gap-2">
        <span class="font-mono text-ui text-foreground" data-testid="scan-repo">{{ scan.repository_name }}</span>
        <FormatBadge :format="scan.repository_format" size="sm" />
      </div>
    </td>
    <td class="px-4 py-3 font-mono text-ui font-medium text-foreground">{{ scan.package_name }}@{{ scan.package_version }}</td>
    <td class="px-4 py-3">
      <div class="flex items-center gap-1.5 text-xs font-mono" :data-counts="countsState">
        <template v-if="countsState === 'counts'">
          <span v-if="scan.critical_count > 0" class="px-1.5 py-0.5 rounded bg-danger-soft text-danger font-semibold">{{ scan.critical_count }}C</span>
          <span v-if="scan.high_count > 0" class="px-1.5 py-0.5 rounded bg-warning-soft text-warning font-semibold">{{ scan.high_count }}H</span>
          <span v-if="scan.medium_count > 0" class="px-1.5 py-0.5 rounded bg-severity-medium/10 text-severity-medium font-semibold">{{ scan.medium_count }}M</span>
          <span v-if="scan.low_count > 0" class="px-1.5 py-0.5 rounded bg-surface-hover text-muted font-semibold">{{ scan.low_count }}L</span>
        </template>
        <span v-else-if="countsState === 'clean'" class="text-success text-xs">{{ t('sec.vuln.clean') }}</span>
        <span v-else-if="countsState === 'partialEmpty'" class="text-warning text-xs">{{ t('sec.vuln.partialEmpty') }}</span>
        <span v-else-if="countsState === 'notScanned'" class="text-danger text-xs">{{ t('sec.vuln.notScanned') }}</span>
        <span v-else class="text-muted text-xs">—</span>
      </div>
    </td>
    <td class="px-4 py-3">
      <div class="flex flex-col items-start gap-1.5">
        <div class="flex items-start gap-1.5">
          <ArrowPathIcon v-if="scan.scan_status === 'scanning'" class="w-3 h-3 mt-1 animate-spin text-warning" aria-hidden="true" />
          <VulnScanStatus :status="scan.scan_status" :scan-error="scan.scan_error" :failed-scanners="scan.failed_scanners" />
        </div>
        <StatusBadge v-if="scan.quarantined" :label="t('sec.vuln.quarantined')" variant="danger" data-testid="scan-quarantined" />
      </div>
    </td>
    <td class="px-4 py-3">
      <div class="flex flex-wrap gap-1">
        <span
          v-for="name in scannerNames(scan)"
          :key="name"
          class="inline-flex items-center px-2 py-0.5 rounded-md text-caption font-semibold border border-border text-muted"
        >{{ name }}</span>
      </div>
    </td>
    <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDateTime(scan.scanned_at || scan.created_at) }}</td>
    <td class="px-4 py-3">
      <div class="flex items-center gap-1.5">
        <AppButton variant="secondary" size="xs" :aria-label="tp('sec.vuln.rowAction', { action: t('sec.vuln.details'), name: scan.package_name })" data-testid="scan-details" @click="emit('details')">
          {{ t('sec.vuln.details') }}
        </AppButton>
        <AppButton variant="secondary" size="xs" :class="{ 'border-danger/30 text-danger bg-danger-soft/40': scan.quarantined }" :disabled="quarantining" :aria-label="tp('sec.vuln.rowAction', { action: quarantineLabel, name: scan.package_name })" @click="emit('quarantine')">
          {{ quarantineLabel }}
        </AppButton>
        <AppButton variant="secondary" size="xs" :aria-label="tp('sec.vuln.rowAction', { action: t('sec.vuln.delete'), name: scan.package_name })" data-testid="scan-delete" @click="emit('delete')">
          {{ t('sec.vuln.delete') }}
        </AppButton>
      </div>
    </td>
  </tr>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import FormatBadge from './FormatBadge.vue'
import StatusBadge from './StatusBadge.vue'
import VulnScanStatus from './VulnScanStatus.vue'
import type { VulnScanRow } from '../api/security'
import { formatDateTime } from '../lib/datetime'
import { vulnCountsState } from '../lib/scanStatus'
import { scannerNames } from '../lib/vulnScanners'
import { t, tp } from '../i18n/en'

const props = defineProps<{
  scan: VulnScanRow
  /** This row's quarantine toggle is in flight. */
  quarantining: boolean
}>()

const emit = defineEmits<{ details: []; quarantine: []; delete: [] }>()

const countsState = computed(() => vulnCountsState(props.scan))
const quarantineLabel = computed(() => (props.scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine')))
</script>
