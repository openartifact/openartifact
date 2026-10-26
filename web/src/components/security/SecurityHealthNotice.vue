<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Tells an administrator what keeps scans from running or finishing: a scanner
  that is not ready (with the server's message and its setup guide), scans that
  failed lately, and scans still running. Nothing renders when all is well.
-->
<template>
  <div v-if="notReady.length || health.scans_failed_24h > 0 || health.scans_pending > 0" class="space-y-2" data-testid="health-notice">
    <div
      v-for="s in notReady"
      :key="s.name"
      class="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning-soft px-4 py-3 text-sm"
      role="status"
      :data-testid="`scanner-not-ready-${s.name}`"
    >
      <ExclamationTriangleIcon class="mt-0.5 h-4 w-4 shrink-0 text-warning" aria-hidden="true" />
      <p class="min-w-0">
        <span class="font-medium text-foreground">{{ tp('security.health.scannerNotReady', { scanner: vulnScannerLabel(s.name) }) }}</span>
        {{ s.message ?? '' }}
        <a
          v-if="safeDocsUrl(s.docs_url)"
          :href="safeDocsUrl(s.docs_url)"
          target="_blank"
          rel="noopener noreferrer"
          class="text-primary underline"
        >{{ t('security.vuln.scannerSetupGuide') }}</a>
      </p>
    </div>
    <p v-if="health.scans_failed_24h > 0" class="text-sm text-warning" role="status" data-testid="scans-failed">
      {{ tpn('security.health.failed', health.scans_failed_24h) }}
    </p>
    <p v-if="health.scans_pending > 0" class="text-sm text-muted" role="status" data-testid="scans-pending">
      {{ tpn('security.health.pending', health.scans_pending) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/24/outline'
import type { SecurityHealth } from '../../api/security'
import { safeDocsUrl, vulnScannerLabel } from '../../lib/vulnScanners'
import { t, tp, tpn } from '../../i18n/en'

const props = defineProps<{ health: SecurityHealth }>()

const notReady = computed(() => props.health.scanners.filter((s) => !s.ready))
</script>
