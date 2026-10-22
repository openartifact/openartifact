<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The one section/tab bar convention: underlined link tabs with an optional
  count. Used by the repository and project headers so both navigate and look
  the same.
-->
<template>
  <nav
    :aria-label="label"
    class="-mt-1 mb-6 flex items-center gap-1 overflow-x-auto border-b border-border"
  >
    <router-link
      v-for="s in items"
      :key="s.id"
      :to="s.to"
      :aria-current="s.id === active ? 'page' : undefined"
      class="relative h-10 px-3 inline-flex items-center gap-1.5 text-sm font-medium shrink-0 transition-colors"
      :class="s.id === active ? 'text-foreground' : 'text-muted hover:text-foreground'"
    >
      {{ s.label }}
      <span
        v-if="s.count !== undefined"
        class="text-micro font-mono px-1.5 py-0.5 rounded"
        :class="s.id === active ? 'bg-primary-soft text-primary' : 'bg-surface-hover text-muted'"
      >{{ s.count }}</span>
      <span
        v-if="s.id === active"
        class="absolute left-2 right-2 -bottom-px h-[2px] bg-primary rounded-full"
        aria-hidden="true"
      />
    </router-link>
  </nav>
</template>

<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'

export interface SectionTab {
  id: string
  label: string
  to: RouteLocationRaw
  /** Item count shown next to the label. */
  count?: number
}

defineProps<{
  items: SectionTab[]
  /** Id of the current section. */
  active: string
  /** Accessible name of the navigation landmark. */
  label: string
}>()
</script>
