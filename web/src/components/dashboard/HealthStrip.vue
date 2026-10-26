<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section v-if="cells.length > 0" :aria-labelledby="'dash-health'" data-testid="health-strip">
    <h2 id="dash-health" class="sr-only">{{ summary }}</h2>
    <ul class="grid grid-cols-1 gap-px overflow-hidden rounded-lg border border-border bg-border sm:grid-cols-2 lg:grid-cols-5" role="list">
      <li v-for="cell in cells" :key="cell.id" class="bg-surface">
        <router-link
          :to="cell.to"
          class="block h-full px-4 py-3 hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary max-md:min-h-11"
        >
          <div class="text-xs text-muted">{{ label(cell) }}</div>
          <div class="mt-1 flex items-center gap-1.5 text-sm font-medium text-foreground">
            <component :is="icon(cell)" class="h-4 w-4 shrink-0" :class="tone(cell)" aria-hidden="true" />
            <span :data-status="cell.status">{{ value(cell) }}</span>
          </div>
          <div class="mt-0.5 truncate text-xs text-muted" :title="detail(cell)">{{ detail(cell) }}</div>
        </router-link>
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircleIcon, ExclamationTriangleIcon, InformationCircleIcon, XCircleIcon } from '@heroicons/vue/20/solid'
import { t, tp, type TranslationKey } from '../../i18n/en'
import { unhealthyCount, type HealthCell } from '../../lib/health'

const props = defineProps<{ cells: HealthCell[] }>()

const summary = computed(() => {
  const bad = unhealthyCount(props.cells)
  return bad === 0 ? t('dashboard.health.summary.ok') : tp('dashboard.health.summary.bad', { count: String(bad) })
})

const key = (cell: HealthCell, part: string): TranslationKey => `dashboard.health.${cell.id}.${part}` as TranslationKey
const label = (cell: HealthCell): string => t(key(cell, 'label'))
const value = (cell: HealthCell): string => tp(key(cell, cell.value), cell.params)
const detail = (cell: HealthCell): string => tp(key(cell, `${cell.value}.detail`), cell.params)

const ICONS = { ok: CheckCircleIcon, warn: ExclamationTriangleIcon, bad: XCircleIcon, info: InformationCircleIcon }
const TONES = { ok: 'text-success', warn: 'text-warning', bad: 'text-danger', info: 'text-muted' }
const icon = (cell: HealthCell) => ICONS[cell.status]
const tone = (cell: HealthCell): string => TONES[cell.status]
</script>
