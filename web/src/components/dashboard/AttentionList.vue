<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <DashSection
    :title="t('dashboard.attention.title')"
    heading-id="dash-attention"
    :state="loading && items.length === 0 ? 'loading' : 'ready'"
    :empty="items.length === 0 && failed === 0"
  >
    <template #badge>
      <span
        v-if="items.length > 0"
        class="rounded-full px-2 py-0.5 text-xs font-medium"
        :class="items.some((i) => i.severity === 'critical') ? 'bg-danger-soft text-danger' : 'bg-warning-soft text-warning'"
        :aria-label="tpn('dashboard.attention.count', items.length)"
      >{{ items.length }}</span>
    </template>
    <template #actions>
      <router-link to="/security" class="text-xs text-primary hover:underline max-md:flex max-md:min-h-11 max-md:items-center" data-testid="attention-security">{{ t('dashboard.attention.openSecurity') }}</router-link>
    </template>
    <template #empty>
      <StateBlock variant="empty" :title="t('dashboard.attention.allClear')" :hint="t('dashboard.attention.allClearHint')" />
    </template>

    <ul class="divide-y divide-border rounded-lg border border-border bg-surface" role="list" data-testid="attention-list">
      <li v-for="item in shown" :key="item.id" class="flex items-center gap-3 px-4 py-3 max-md:min-h-11">
        <component :is="icon(item)" class="h-5 w-5 shrink-0" :class="tone(item)" aria-hidden="true" />
        <div class="min-w-0 flex-1">
          <router-link :to="item.to" class="text-sm font-medium text-foreground hover:underline">{{ text(item) }}</router-link>
          <div class="text-xs text-muted">
            {{ tDynamic(`dashboard.attention.severity.${item.severity}`) }}<template v-if="item.params.message"> · {{ item.params.message }}</template>
          </div>
        </div>
        <router-link :to="item.to" :aria-label="`${t('dashboard.attention.open')}: ${text(item)}`" class="shrink-0 text-xs text-primary hover:underline max-md:flex max-md:min-h-11 max-md:items-center">{{ t('dashboard.attention.open') }}</router-link>
      </li>
      <li v-if="items.length === 0" class="px-4 py-3 text-sm text-muted">{{ t('dashboard.attention.allClear') }}</li>
    </ul>
    <p v-if="failed > 0" class="mt-2 text-xs text-muted" role="status">{{ t('dashboard.attention.incomplete') }}</p>
    <AppButton v-if="items.length > LIMIT" variant="ghost" size="sm" class="mt-2 max-md:min-h-11" :aria-expanded="expanded" @click="expanded = !expanded">
      {{ expanded ? t('dashboard.attention.showFewer') : tp('dashboard.attention.showMore', { count: String(items.length - LIMIT) }) }}
    </AppButton>
  </DashSection>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ExclamationTriangleIcon, InformationCircleIcon, XCircleIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import StateBlock from '../ui/StateBlock.vue'
import DashSection from './DashSection.vue'
import { t, tDynamic, tp, tpn, type TranslationKey } from '../../i18n'
import type { AttentionItem } from '../../lib/attention'

const LIMIT = 5

const props = defineProps<{ items: AttentionItem[]; loading: boolean; failed: number }>()

const expanded = ref(false)
const shown = computed(() => (expanded.value ? props.items : props.items.slice(0, LIMIT)))

const text = (item: AttentionItem): string => tp(`dashboard.attention.${item.kind}` as TranslationKey, item.params)
const ICONS = { critical: XCircleIcon, high: ExclamationTriangleIcon, medium: ExclamationTriangleIcon, low: InformationCircleIcon }
const TONES = { critical: 'text-danger', high: 'text-warning', medium: 'text-warning', low: 'text-muted' }
const icon = (item: AttentionItem) => ICONS[item.severity]
const tone = (item: AttentionItem): string => TONES[item.severity]
</script>
