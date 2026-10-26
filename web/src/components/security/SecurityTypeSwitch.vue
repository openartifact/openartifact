<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div role="group" :aria-label="t('security.type.label')" class="inline-flex max-w-full flex-wrap gap-1 rounded-lg bg-surface-hover p-0.5" data-testid="type-switch">
    <button
      v-for="ty in TYPE_FILTERS"
      :key="ty"
      type="button"
      :aria-pressed="modelValue === ty"
      class="inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
      :class="modelValue === ty ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
      :data-testid="`type-${ty}`"
      @click="emit('update:modelValue', ty)"
    >
      {{ tDynamic(`security.type.${ty}`) }}
      <span v-if="countOf(ty) !== undefined" class="font-mono text-micro tabular-nums">{{ countOf(ty) }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import type { SecuritySummary } from '../../api/security'
import { TYPE_FILTERS, typeTotal, type TypeFilter } from '../../lib/securityFindings'
import { t, tDynamic } from '../../i18n'

const props = defineProps<{
  modelValue: TypeFilter
  /** Counts per type come from the summary (repository filter applied). */
  summary: SecuritySummary | null
}>()

const emit = defineEmits<{ 'update:modelValue': [type: TypeFilter] }>()

const countOf = (ty: TypeFilter): number | undefined => typeTotal(props.summary, ty)
</script>
