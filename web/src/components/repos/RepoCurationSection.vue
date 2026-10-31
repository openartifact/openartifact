<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Package curation of a proxy repository: the mode that decides what the rules mean (off, denylist, allowlist)
  and the rules themselves, which say which upstream packages and versions the proxy may serve.
-->
<template>
  <section class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('curation.title')" data-testid="curation-section">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h2 class="text-sm font-semibold text-foreground">{{ t('curation.title') }}</h2>
        <p class="mt-1 max-w-2xl text-sm text-muted">{{ t('curation.subtitle') }}</p>
      </div>
      <AppButton variant="secondary" size="sm" data-testid="rule-add" @click="openAdd()">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('curation.add') }}
      </AppButton>
    </div>

    <div role="radiogroup" :aria-label="t('curation.mode.label')" class="grid gap-2 sm:grid-cols-3">
      <button
        v-for="m in CURATION_MODES"
        :key="m"
        type="button"
        role="radio"
        :aria-checked="mode === m"
        :disabled="savingMode"
        :data-testid="`mode-${m}`"
        class="min-h-11 rounded-lg border px-3 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-60"
        :class="mode === m ? 'border-primary bg-primary-soft' : 'border-border-strong bg-surface hover:bg-surface-hover'"
        @click="changeMode(m)"
      >
        <span class="block text-sm font-medium text-foreground">{{ modeText(m).label }}</span>
        <span class="block text-xs text-muted">{{ modeText(m).hint }}</span>
      </button>
    </div>

    <p v-if="mode === 'off' && rules.length > 0" class="text-xs text-muted" data-testid="curation-inactive">{{ t('curation.inactive') }}</p>
    <p v-if="allowlistEmpty && !loading" class="text-xs text-warning" data-testid="allowlist-empty">{{ t('curation.allowlistEmpty') }}</p>

    <div v-if="showEmpty" class="rounded-lg border border-border px-5 py-6" data-testid="curation-empty">
      <p class="text-sm font-medium text-foreground">{{ t('curation.empty') }}</p>
      <p class="mt-1 max-w-2xl text-xs text-muted">{{ t('curation.emptyHint') }}</p>
      <dl class="mt-3 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-xs">
        <template v-for="m in CURATION_MODES" :key="m">
          <dt class="font-medium text-foreground">{{ modeText(m).label }}</dt>
          <dd class="text-muted">{{ modeText(m).hint }}</dd>
        </template>
      </dl>
      <p class="mt-3 max-w-2xl text-xs text-muted">{{ t('curation.exemptsMinAge') }}</p>
    </div>
    <div v-else :class="{ 'opacity-60': mode === 'off' }" data-testid="curation-rules">
      <ListTable
        :columns="columns"
        :caption="t('curation.title')"
        :rows="rules"
        :loading="loading"
        :error="loadError"
        :total="rules.length"
        :limit="Math.max(rules.length, 1)"
        @retry="load"
      >
        <template #cell-action="{ row }">
          <StatusBadge :label="actionText(row.action).label" :variant="row.action === 'block' ? 'danger' : 'success'" />
        </template>
        <template #cell-package="{ row }">
          <span class="block truncate font-mono text-sm text-foreground" data-testid="rule-package" :title="row.package_pattern">{{ row.package_pattern }}</span>
        </template>
        <template #cell-version="{ row }">
          <span class="font-mono text-sm" :class="row.version_pattern === '' ? 'font-sans text-muted' : 'text-foreground'">{{ versionPatternText(row) }}</span>
        </template>
        <template #cell-reason="{ row }">
          <span class="text-sm text-muted">{{ row.reason || '—' }}</span>
        </template>
        <template #cell-created="{ row }">
          <span class="text-xs text-muted">{{ row.created_by || '—' }} · <time :datetime="row.created_at">{{ formatDate(row.created_at) }}</time></span>
        </template>
        <template #cell-actions="{ row }">
          <RowActions :actions="rowActions" :subject="row.package_pattern" @action="(key) => onAction(key, row)" />
        </template>
      </ListTable>
    </div>

    <RepoCurationRuleDrawer
      v-model="form"
      :open="drawerOpen"
      :editing="editing !== null"
      :saving="saving"
      :valid="formValid"
      :error="formError"
      @close="closeDrawer"
      @save="save"
    />

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('curation.deleteTitle')"
      :description="deleteTarget?.package_pattern ?? ''"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    >
      <p class="text-sm text-muted">{{ t('curation.deleteBody') }}</p>
    </ConfirmDialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { PencilSquareIcon, PlusIcon, TrashIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import StatusBadge from '../StatusBadge.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RowActions, { type RowAction } from '../ui/RowActions.vue'
import RepoCurationRuleDrawer from './RepoCurationRuleDrawer.vue'
import type { CurationRepository, CurationRule } from '../../api/curation'
import { useCurationRules } from '../../composables/useCurationRules'
import { actionText, CURATION_MODES, curationModeOf, modeText, versionPatternText } from '../../lib/curation'
import { formatDate } from '../../lib/datetime'
import { t } from '../../i18n'

const props = defineProps<{ repo: CurationRepository }>()
const emit = defineEmits<{ saved: [repo: CurationRepository] }>()

const {
  mode,
  savingMode,
  rules,
  loading,
  loadError,
  allowlistEmpty,
  load,
  changeMode,
  drawerOpen,
  editing,
  form,
  formValid,
  saving,
  formError,
  openAdd,
  openEdit,
  closeDrawer,
  save,
  deleteTarget,
  deleting,
  confirmDelete,
} = useCurationRules(
  () => props.repo.name,
  () => curationModeOf(props.repo),
  (updated) => emit('saved', updated),
)

const showEmpty = computed(() => !loading.value && loadError.value === '' && rules.value.length === 0)

const columns = computed<ListColumn[]>(() => [
  { key: 'action', label: t('curation.col.action') },
  { key: 'package', label: t('curation.col.package'), grow: true },
  { key: 'version', label: t('curation.col.version') },
  { key: 'reason', label: t('curation.col.reason'), dropOrder: 2 },
  { key: 'created', label: t('curation.col.created'), dropOrder: 1 },
  { key: 'actions', label: t('ui.table.actions'), actions: true },
])

const rowActions = computed<RowAction[]>(() => [
  { key: 'edit', label: t('common.edit'), icon: PencilSquareIcon },
  { key: 'delete', label: t('common.delete'), icon: TrashIcon, tone: 'danger' },
])

function onAction(key: string, rule: CurationRule): void {
  if (key === 'edit') openEdit(rule)
  else if (key === 'delete') deleteTarget.value = rule
}

onMounted(load)
</script>
