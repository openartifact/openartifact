<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('sec.vex.title')" data-testid="vex-statements">
    <div class="px-4 py-3 border-b border-border flex items-center justify-between gap-3 flex-wrap">
      <div class="min-w-0">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('sec.vex.title') }}</h3>
        <p class="text-xs text-muted">{{ t('sec.vex.description') }}</p>
      </div>
      <div class="flex items-center gap-3">
        <span v-if="repo && !loading && !loadError" class="text-caption font-mono text-muted" data-testid="vex-count">
          {{ statements.length }} {{ t('sec.vex.count') }}
        </span>
        <VexDownloadButtons v-if="repo" :repo="repo" size="sm" test-id-prefix="vex-repo-download" />
      </div>
    </div>

    <p v-if="!repo" class="px-4 py-8 text-center text-sm text-muted" data-testid="vex-select-repo">{{ t('sec.vex.selectRepo') }}</p>

    <div v-else-if="loading" class="py-10 flex flex-col items-center gap-3 text-muted" role="status">
      <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
      <span class="text-sm">{{ t('sec.vex.loading') }}</span>
    </div>

    <div v-else-if="loadError" class="py-10 text-center" role="alert">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <AppButton variant="secondary" size="sm" @click="reload">{{ t('sec.common.retry') }}</AppButton>
    </div>

    <p v-else-if="statements.length === 0" class="px-4 py-8 text-center text-sm text-muted" data-testid="vex-statements-empty">
      {{ t('sec.vex.empty') }}
    </p>

    <div v-else class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="border-b border-border bg-surface-hover/40">
          <tr>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vex.col.artifact') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vex.col.vuln') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vex.col.status') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vex.col.justification') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vex.col.impact') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vex.col.updated') }}</th>
            <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vex.col.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="s in statements"
            :key="s.id"
            class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0 align-top"
            data-testid="vex-statement-row"
          >
            <td class="px-4 py-3 font-mono text-ui text-foreground" :title="s.artifact_id">{{ artifactLabels?.[s.artifact_id] ?? s.artifact_id }}</td>
            <td class="px-4 py-3 font-mono text-ui">{{ s.vuln_id }}</td>
            <td class="px-4 py-3"><VexStatusBadge :status="s.status" /></td>
            <td class="px-4 py-3 text-xs text-muted">{{ s.justification ? vexJustificationLabel(s.justification) : '—' }}</td>
            <td class="px-4 py-3 text-xs text-muted max-w-xs"><span class="line-clamp-2" :title="s.impact_statement">{{ s.impact_statement || '—' }}</span></td>
            <td class="px-4 py-3 text-xs text-muted font-mono">{{ s.updated_at ? formatDateTime(s.updated_at) : '—' }}</td>
            <td class="px-4 py-3">
              <AppButton
                variant="secondary"
                size="xs"
                :aria-label="`${t('sec.vex.delete')}: ${s.vuln_id}`"
                data-testid="vex-statement-delete"
                @click="deleteTarget = s"
              >
                {{ t('sec.vex.delete') }}
              </AppButton>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('sec.vex.deleteTitle')"
      :description="deleteTarget ? `${deleteTarget.vuln_id} · ${artifactLabels?.[deleteTarget.artifact_id] ?? deleteTarget.artifact_id}` : ''"
      :confirm-label="t('sec.vex.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    >
      {{ t('sec.vex.deleteDescription') }}
    </ConfirmDialog>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import VexDownloadButtons from './VexDownloadButtons.vue'
import VexStatusBadge from './VexStatusBadge.vue'
import { deleteVexStatement, listVexStatements, type VexRecord } from '../api/vex'
import { formatDateTime } from '../lib/datetime'
import { vexJustificationLabel } from '../lib/vex'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{
  /** Repository whose decisions are listed; empty shows a hint. */
  repo: string
  /** Display name (`name@version`) per `artifact_id`, for the artifacts the page knows. */
  artifactLabels?: Record<string, string>
}>()

const emit = defineEmits<{
  /** A decision was deleted from the list. */
  (e: 'changed'): void
}>()

const toasts = useToastStore()

const statements = ref<VexRecord[]>([])
const loading = ref(false)
const loadError = ref('')
// Bumped by every (re)load and on unmount; a response carrying an older value is discarded.
let seq = 0

async function reload(): Promise<void> {
  const mine = ++seq
  statements.value = []
  loadError.value = ''
  if (!props.repo) {
    loading.value = false
    return
  }
  loading.value = true
  try {
    const list = await listVexStatements(props.repo)
    if (mine !== seq) return
    statements.value = list
  } catch (err) {
    if (mine !== seq) return
    loadError.value = err instanceof Error ? err.message : t('sec.vex.loadFailed')
  } finally {
    if (mine === seq) loading.value = false
  }
}

watch(() => props.repo, () => void reload(), { immediate: true })

const deleteTarget = ref<VexRecord | null>(null)
const deleting = ref(false)

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleting.value) return
  deleting.value = true
  try {
    await deleteVexStatement(props.repo, target.id)
    statements.value = statements.value.filter((s) => s.id !== target.id)
    deleteTarget.value = null
    toasts.success(t('sec.vex.deleted'))
    emit('changed')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vex.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

onBeforeUnmount(() => {
  seq++
})

/** Reloads the list; the page calls it after it recorded or deleted a decision elsewhere. */
defineExpose({ reload })
</script>
