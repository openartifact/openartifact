<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Stored size of a project in the list, with a bar against the size limit when
  one is set. Without a limit the project is not metered, which is said in words.
-->
<template>
  <span v-if="usage?.size_bytes === undefined" class="text-xs text-muted">—</span>
  <div v-else-if="share" class="min-w-32">
    <p class="text-xs tabular-nums text-foreground">
      {{ formatBytes(usage.size_bytes ?? 0) }}
      <span class="text-muted">{{ tp('projects.usage.sizeOfLimit', { limit: formatBytes(quota?.limit_bytes ?? 0) }) }}</span>
    </p>
    <div
      class="mt-1 h-1.5 overflow-hidden rounded-full bg-surface-hover"
      role="progressbar"
      :aria-label="t('projects.column.used')"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="Math.min(share.percent, 100)"
      :aria-valuetext="tp('projects.usage.percentUsed', { n: String(share.percent) })"
    >
      <div class="h-full rounded-full" :class="BAR[share.level]" :style="{ width: `${Math.min(share.percent, 100)}%` }" />
    </div>
    <p v-if="share.level !== 'ok'" class="mt-0.5 text-micro font-medium" :class="share.level === 'full' ? 'text-danger' : 'text-warning'">
      {{ tp('projects.usage.percentUsed', { n: String(share.percent) }) }}
    </p>
  </div>
  <p v-else class="text-xs tabular-nums text-foreground">
    {{ formatBytes(usage.size_bytes ?? 0) }}
    <span class="text-muted">· {{ t('projects.list.noQuota') }}</span>
  </p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectListItem } from '../api/projects'
import { formatBytes } from '../lib/quota'
import { quotaUsage, type QuotaLevel } from '../lib/projectList'
import { t, tp } from '../i18n'

const props = defineProps<{
  usage?: ProjectListItem['usage']
  quota?: ProjectListItem['quota']
}>()

const BAR: Record<QuotaLevel, string> = { ok: 'bg-primary', warn: 'bg-warning', full: 'bg-danger' }
const share = computed(() => quotaUsage(props.usage?.size_bytes, props.quota?.limit_bytes))
</script>
