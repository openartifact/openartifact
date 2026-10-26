<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <div v-if="loading" class="flex justify-center py-8 text-muted" role="status">
      <ArrowPathIcon class="h-5 w-5 animate-spin" aria-hidden="true" />
      <span class="sr-only">{{ t('security.detail.loading') }}</span>
    </div>
    <p v-else-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <div v-else-if="scan" class="space-y-4" data-testid="secret-detail">
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
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import SeverityBadge, { type Severity } from '../ui/SeverityBadge.vue'
import { getSecretScan, type SecretScan, type SecurityFinding } from '../../api/security'
import { t } from '../../i18n/en'

const props = defineProps<{ finding: SecurityFinding }>()

const scan = ref<SecretScan | null>(null)
const loading = ref(false)
const error = ref('')
let seq = 0

const isSeverity = (s: string): s is Severity => ['critical', 'high', 'medium', 'low'].includes(s)

watch(
  () => props.finding.id,
  async () => {
    const mine = ++seq
    scan.value = null
    error.value = ''
    loading.value = true
    try {
      const result = await getSecretScan(props.finding.repository, props.finding.id)
      if (mine === seq) scan.value = result
    } catch (err) {
      if (mine === seq) error.value = err instanceof Error ? err.message : t('security.secret.loadFailed')
    } finally {
      if (mine === seq) loading.value = false
    }
  },
  { immediate: true },
)
</script>
