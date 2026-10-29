<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <FindingDetailShell :loading="loading" :error="error">
    <div v-if="scan" class="space-y-4" data-testid="secret-detail">
      <p class="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger">{{ t('security.secret.rotateFirst') }}</p>
      <p v-if="scan.scan_error" class="text-sm text-danger" role="alert">{{ scan.scan_error }}</p>
      <ul v-if="scan.findings?.length" class="space-y-3">
        <li v-for="(f, i) in scan.findings" :key="i" class="space-y-1" data-testid="secret-finding">
          <div class="flex flex-wrap items-center gap-2 text-sm">
            <span class="font-semibold text-foreground">{{ f.type }}</span>
            <SeverityBadge v-if="isSeverity(f.severity)" :severity="f.severity" />
            <span v-if="f.line_hint" class="text-xs text-muted">{{ f.line_hint }}</span>
          </div>
          <p v-if="f.description" class="text-xs text-muted">{{ f.description }}</p>
          <p v-if="f.redacted_value" class="break-all rounded-md border border-border bg-surface-hover px-3 py-2 font-mono text-sm" data-testid="secret-mask">{{ f.redacted_value }}</p>
        </li>
      </ul>
      <p v-else class="text-sm text-muted">{{ t('security.secret.noFindings') }}</p>
    </div>
  </FindingDetailShell>
</template>

<script setup lang="ts">
import FindingDetailShell from './FindingDetailShell.vue'
import SeverityBadge, { type Severity } from '../ui/SeverityBadge.vue'
import { getSecretScan, type SecurityFinding } from '../../api/security'
import { useFindingScan } from '../../composables/useFindingScan'
import { t } from '../../i18n'

const props = defineProps<{ finding: SecurityFinding }>()

const isSeverity = (s: string): s is Severity => ['critical', 'high', 'medium', 'low'].includes(s)

const { scan, loading, error } = useFindingScan(
  () => props.finding,
  (f) => getSecretScan(f.repository, f.id),
  () => t('security.secret.loadFailed'),
)
</script>
