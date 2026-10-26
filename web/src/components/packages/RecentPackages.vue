<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ul class="divide-y divide-border rounded-lg border border-border bg-surface" role="list" data-testid="recent-packages">
    <li v-for="p in items" :key="`${p.repository}/${p.name}/${p.version}`" class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 text-sm max-md:min-h-11">
      <FormatBadge :format="p.format" size="sm" />
      <router-link :to="artifactPath(p.repository, p.name, p.version)" class="min-w-0 truncate font-medium text-foreground hover:underline">{{ p.name }}</router-link>
      <span class="font-mono text-xs text-muted">{{ p.version }}</span>
      <router-link :to="`/repos/${encodeURIComponent(p.repository)}`" class="text-xs text-muted hover:underline">{{ p.repository }}</router-link>
      <time class="ml-auto shrink-0 text-xs tabular-nums text-muted" :datetime="p.created_at" :title="formatDateTime(p.created_at)">{{ timeAgo(p.created_at) }}</time>
    </li>
  </ul>
</template>

<script setup lang="ts">
import type { RecentPackage } from '../../api/dashboard'
import FormatBadge from '../FormatBadge.vue'
import { artifactPath } from '../../lib/artifactPath'
import { formatDateTime, timeAgo } from '../../lib/datetime'

defineProps<{ items: RecentPackage[] }>()
</script>
