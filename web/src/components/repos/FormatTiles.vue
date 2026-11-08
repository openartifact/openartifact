<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Format picker: one tile per format, a radio group. Tiles carry the format icon and the full
  label directly (no badge pill inside the tile) and size to the container, so a long label such
  as "Arch Linux (pacman)" wraps inside its tile instead of being clipped.
-->
<template>
  <div role="radiogroup" :aria-label="label" class="grid grid-cols-[repeat(auto-fill,minmax(7.5rem,1fr))] gap-2">
    <button
      v-for="f in tiles"
      :key="f.id"
      type="button"
      role="radio"
      :aria-checked="model === f.id"
      :disabled="disabled"
      class="flex min-h-11 min-w-0 items-center gap-2 rounded-lg border px-2.5 py-2 text-left text-sm font-medium text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-60"
      :class="model === f.id ? 'border-primary bg-primary-soft' : 'border-border-strong bg-surface hover:bg-surface-hover'"
      @click="model = f.id"
    >
      <span
        class="inline-flex h-4 w-4 shrink-0 items-center justify-center text-[color:var(--fmt)] dark:text-[color:var(--fmt-dark)]"
        :style="{ '--fmt': f.colors.light, '--fmt-dark': f.colors.dark }"
        aria-hidden="true"
      >
        <svg v-if="f.icon" :viewBox="f.viewBox" class="h-4 w-4"><path :d="f.icon" fill="currentColor" :fill-rule="f.evenodd ? 'evenodd' : undefined" /></svg>
        <span v-else class="h-3 w-3 rounded-sm bg-[color:var(--fmt)] dark:bg-[color:var(--fmt-dark)]"></span>
      </span>
      <span class="min-w-0 break-words leading-tight">{{ f.label }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatIcon, formatIconEvenOdd, formatLabel, formatThemeColors, formatViewBox } from '../../lib/formats'
import type { FormatId } from '../../lib/formats'

const props = defineProps<{ formats: FormatId[]; label: string; disabled?: boolean }>()
const model = defineModel<FormatId>({ required: true })

const tiles = computed(() =>
  props.formats.map((id) => ({
    id,
    label: formatLabel(id),
    colors: formatThemeColors(id),
    icon: formatIcon(id),
    evenodd: formatIconEvenOdd(id),
    viewBox: formatViewBox(id),
  })),
)
</script>
