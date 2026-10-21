<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="inline-flex flex-col items-start gap-1">
    <StatusBadge :label="vulnScanLabel(status)" :variant="vulnScanVariant(status)" data-testid="scan-status" />
    <template v-if="vulnScanHasFailures(status)">
      <p v-if="failedScanners.length" class="text-[11px] text-muted" data-testid="failed-scanners">
        {{ t('sec.vuln.failedScanners') }}: <span class="font-mono">{{ failedScanners.join(', ') }}</span>
      </p>
      <p
        v-if="scanError"
        class="text-[11px] max-w-xs break-words"
        :class="status === 'failed' ? 'text-danger' : 'text-warning'"
        data-testid="scan-error"
      >{{ scanError }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import StatusBadge from './StatusBadge.vue'
import { vulnScanHasFailures, vulnScanLabel, vulnScanVariant } from '../lib/scanStatus'
import { t } from '../i18n/en'

const props = defineProps<{
  status: string
  /** Sanitized summary of the failing scanners (never raw scanner output). */
  scanError?: string | null
  failedScanners?: readonly string[] | null
}>()

const failedScanners = computed<readonly string[]>(() => props.failedScanners ?? [])
</script>
