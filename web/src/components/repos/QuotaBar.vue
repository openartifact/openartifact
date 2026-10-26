<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Used bytes against a size limit; without a limit the repository is not metered, which is said in words. -->
<template>
  <p v-if="share === null" class="text-xs text-muted">{{ t('quota.unlimited') }}</p>
  <div v-else class="min-w-28" data-testid="quota-bar">
    <p class="text-xs tabular-nums text-foreground">
      {{ formatBytes(used ?? 0) }}
      <span class="text-muted">{{ tp('rl.quota.of', { limit: formatBytes(limit ?? 0) }) }}</span>
    </p>
    <div
      class="mt-1 h-1.5 overflow-hidden rounded-full bg-surface-hover"
      role="progressbar"
      :aria-label="t('quota.usage')"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="Math.min(share.percent, 100)"
      :aria-valuetext="tp('quota.percentUsed', { n: String(share.percent) })"
    >
      <div class="h-full rounded-full" :class="BAR[share.level]" :style="{ width: `${Math.min(share.percent, 100)}%` }" />
    </div>
    <p class="mt-0.5 text-micro" :class="share.level === 'full' ? 'font-medium text-danger' : share.level === 'warn' ? 'font-medium text-warning' : 'text-muted'">
      {{ tp('quota.percentUsed', { n: String(share.percent) }) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatBytes } from '../../lib/quota'
import { quotaUsage, type QuotaLevel } from '../../lib/projectList'
import { t, tp } from '../../i18n/en'

const props = defineProps<{ used?: number; limit?: number }>()

const BAR: Record<QuotaLevel, string> = { ok: 'bg-primary', warn: 'bg-warning', full: 'bg-danger' }
const share = computed(() => quotaUsage(props.used, props.limit))
</script>
