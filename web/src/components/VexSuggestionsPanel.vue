<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- VEX decisions the federated peers offer to one repository: an administrator accepts (records it here) or dismisses each. -->
<template>
  <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('security.vexSuggest.title')" data-testid="vex-suggestions">
    <div class="px-4 py-3 border-b border-border flex items-center justify-between gap-3 flex-wrap">
      <div class="min-w-0">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('security.vexSuggest.title') }}</h3>
        <p class="text-xs text-muted">{{ t('security.vexSuggest.description') }}</p>
      </div>
      <label class="flex items-center gap-2 text-xs text-muted">
        {{ t('security.vexSuggest.stateFilter') }}
        <select v-model="state" :class="SELECT" data-testid="vex-suggestions-state">
          <option v-for="s in VEX_SUGGESTION_FILTERS" :key="s" :value="s">{{ tDynamic(`security.vexSuggest.state.${s}`) }}</option>
        </select>
      </label>
    </div>

    <div v-if="loading" class="py-10 flex flex-col items-center gap-3 text-muted" role="status">
      <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
      <span class="text-sm">{{ t('security.vexSuggest.loading') }}</span>
    </div>

    <StateBlock v-else-if="loadError" variant="error" :hint="loadError" :level="3">
      <template #action>
        <AppButton variant="secondary" size="sm" @click="reload">{{ t('security.common.retry') }}</AppButton>
      </template>
    </StateBlock>

    <StateBlock
      v-else-if="suggestions.length === 0"
      :title="t('security.vexSuggest.empty')"
      :level="3"
      data-testid="vex-suggestions-empty"
    />

    <TableScroller v-else>
      <table class="w-full text-sm">
        <thead class="border-b border-border">
          <tr>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('security.vex.col.artifact') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('security.vex.col.vuln') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('security.vexSuggest.col.suggested') }}</th>
            <th scope="col" data-drop-order="2" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('security.vexSuggest.col.peer') }}</th>
            <th scope="col" data-drop-order="1" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('security.vexSuggest.col.local') }}</th>
            <th scope="col" class="text-right px-4 py-2.5 text-muted font-medium" :class="ACTIONS_HEAD">{{ t('security.vex.col.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="s in suggestions"
            :key="s.id"
            class="group/row hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0 align-top"
            data-testid="vex-suggestion-row"
          >
            <td class="truncate px-4 py-3 font-mono text-ui text-foreground" :class="GROW_CELL" :title="s.artifact_id">
              {{ artifactLabels?.[s.artifact_id] ?? s.artifact_id }}
            </td>
            <td class="px-4 py-3 font-mono text-ui">{{ s.vuln_id }}</td>
            <td class="px-4 py-3 text-xs">
              <VexStatusBadge :status="s.status" />
              <p v-if="s.justification" class="mt-1 text-muted">{{ vexJustificationLabel(s.justification) }}</p>
              <p v-if="s.impact_statement" class="mt-1 text-muted line-clamp-2" :title="s.impact_statement">{{ s.impact_statement }}</p>
              <p v-if="s.state !== 'pending'" class="mt-1 text-muted" data-testid="vex-suggestion-state">
                {{ tDynamic(`security.vexSuggest.state.${s.state}`) }}<template v-if="s.decided_at"> · {{ formatDateTime(s.decided_at) }}</template>
              </p>
            </td>
            <td class="px-4 py-3 text-xs max-w-xs">
              <p class="text-foreground">{{ s.peer_name }}</p>
              <p class="truncate font-mono text-muted" :title="s.peer_base_url">{{ s.peer_base_url }}</p>
            </td>
            <td class="px-4 py-3 text-xs">
              <template v-if="s.local_status">
                <VexStatusBadge :status="s.local_status as VexStatus" />
                <p class="mt-1 text-muted">{{ tDynamic(`security.vex.source.${s.local_source_kind || 'local'}`) }}</p>
              </template>
              <span v-else class="text-muted">{{ t('security.vexSuggest.noLocal') }}</span>
            </td>
            <td class="px-4 py-3" :class="ACTIONS_CELL">
              <RowActions :actions="actionsOf(s)" :subject="s.vuln_id" @action="(key) => onAction(key, s)" />
            </td>
          </tr>
        </tbody>
      </table>
    </TableScroller>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { ArrowPathIcon, CheckIcon, XMarkIcon } from '@heroicons/vue/20/solid'
import AppButton from './AppButton.vue'
import StateBlock from './ui/StateBlock.vue'
import RowActions, { type RowAction } from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import VexStatusBadge from './VexStatusBadge.vue'
import {
  acceptVexSuggestion,
  dismissVexSuggestion,
  listVexSuggestions,
  VEX_SUGGESTION_FILTERS,
  type VexStatus,
  type VexSuggestion,
  type VexSuggestionState,
} from '../api/vex'
import { formatDateTime } from '../lib/datetime'
import { vexJustificationLabel } from '../lib/vex'
import { useToastStore } from '../stores/toast'
import { t, tDynamic } from '../i18n'

const props = defineProps<{
  repo: string
  /** Display name (`name@version`) per `artifact_id`, for the artifacts the page knows. */
  artifactLabels?: Record<string, string>
}>()

const emit = defineEmits<{
  /** A suggestion became this repository's decision: the decisions list is stale. */
  (e: 'accepted'): void
}>()

const SELECT = 'h-8 rounded-lg border border-border-strong bg-surface px-2 text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-primary'

const toasts = useToastStore()

const state = ref<VexSuggestionState | 'all'>('pending')
const suggestions = ref<VexSuggestion[]>([])
const loading = ref(false)
const loadError = ref('')
const busy = ref<string | null>(null)
// Bumped by every (re)load and on unmount; a response carrying an older value is discarded.
let seq = 0

async function reload(): Promise<void> {
  const mine = ++seq
  loadError.value = ''
  loading.value = true
  try {
    const list = await listVexSuggestions(props.repo, state.value)
    if (mine !== seq) return
    suggestions.value = list
  } catch (err) {
    if (mine !== seq) return
    suggestions.value = []
    loadError.value = err instanceof Error ? err.message : t('security.vexSuggest.loadFailed')
  } finally {
    if (mine === seq) loading.value = false
  }
}

watch(() => [props.repo, state.value], () => void reload(), { immediate: true })

function actionsOf(s: VexSuggestion): RowAction[] {
  const pending = busy.value === s.id
  return [
    { key: 'accept', label: t('security.vexSuggest.accept'), icon: CheckIcon, disabled: pending || s.state === 'accepted', loading: pending },
    { key: 'dismiss', label: t('security.vexSuggest.dismiss'), icon: XMarkIcon, tone: 'danger', disabled: pending || s.state !== 'pending' },
  ]
}

async function onAction(key: string, s: VexSuggestion): Promise<void> {
  if (busy.value) return
  busy.value = s.id
  try {
    if (key === 'accept') {
      await acceptVexSuggestion(props.repo, s.id)
      toasts.success(t('security.vexSuggest.accepted'))
      emit('accepted')
    } else {
      await dismissVexSuggestion(props.repo, s.id)
      toasts.success(t('security.vexSuggest.dismissed'))
    }
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : key === 'accept' ? t('security.vexSuggest.acceptFailed') : t('security.vexSuggest.dismissFailed'))
  } finally {
    busy.value = null
  }
  await reload()
}

onBeforeUnmount(() => {
  seq++
})
</script>
