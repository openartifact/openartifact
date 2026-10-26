<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Format picker: one tile per format, a radio group. -->
<template>
  <div role="radiogroup" :aria-label="label" class="grid grid-cols-2 gap-2 sm:grid-cols-4">
    <button
      v-for="f in formats"
      :key="f"
      type="button"
      role="radio"
      :aria-checked="model === f"
      :disabled="disabled"
      class="flex min-h-11 items-center justify-center rounded-lg border px-2 py-2 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-60"
      :class="model === f ? 'border-primary bg-primary-soft' : 'border-border-strong bg-surface hover:bg-surface-hover'"
      @click="model = f"
    >
      <FormatBadge :format="f" />
    </button>
  </div>
</template>

<script setup lang="ts">
import FormatBadge from '../FormatBadge.vue'
import type { FormatId } from '../../lib/formats'

defineProps<{ formats: FormatId[]; label: string; disabled?: boolean }>()
const model = defineModel<FormatId>({ required: true })
</script>
