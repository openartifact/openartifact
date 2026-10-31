<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <CardSection :title="t('tiering.policy.title')" :description="t('tiering.policy.description')">
    <template #action>
      <AppButton size="sm" :disabled="tiers.length === 0" @click="openEditor(null)">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('tiering.policy.addTitle') }}
      </AppButton>
    </template>

    <div v-if="policies.length === 0" class="py-12 text-center">
      <p class="text-sm font-medium text-foreground">{{ t('tiering.policy.empty') }}</p>
      <p class="text-xs text-muted mt-1">{{ t('tiering.policy.emptyHint') }}</p>
    </div>

    <TableScroller v-else>
      <table class="w-full" data-testid="policy-table">
        <thead>
          <tr class="bg-surface-hover/30 text-left text-caption font-semibold text-muted">
            <th scope="col" data-drop-order="1" class="px-5 py-2.5 text-right">{{ t('tiering.policy.column.position') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.name') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.enabled') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.conditions') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.route') }}</th>
            <th scope="col" class="px-5 py-2.5 text-right" :class="ACTIONS_HEAD">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="p in policies" :key="p.id" class="group/row" data-testid="policy-row">
            <td class="px-5 py-3 text-right tabular-nums text-sm text-muted">{{ p.position }}</td>
            <td class="truncate px-3 py-3 text-sm font-medium text-foreground" :class="GROW_CELL" :title="p.name">{{ p.name }}</td>
            <td class="px-3 py-3">
              <AppToggle
                :model-value="!!p.enabled"
                :label="`${t('tiering.policy.column.enabled')}: ${p.name}`"
                :disabled="busy.has(p.id ?? '')"
                @update:model-value="setEnabled(p, $event)"
              />
            </td>
            <td class="px-3 py-3">
              <div class="flex flex-wrap items-center gap-1.5 text-xs text-foreground">
                <span v-for="c in policyConditions(p)" :key="c" class="px-1.5 py-0.5 rounded-md bg-surface-hover">{{ c }}</span>
                <FormatBadge v-for="f in p.formats ?? []" :key="f" :format="f" size="sm" />
              </div>
            </td>
            <td class="px-3 py-3 font-mono text-xs">
              {{ p.from_tier ?? t('tiering.policy.anyTier') }} → {{ p.target_tier }}
            </td>
            <td class="px-5 py-3" :class="ACTIONS_CELL">
              <RowActions :actions="policyActions" :subject="p.name" @action="(key) => onAction(key, p)" />
            </td>
          </tr>
        </tbody>
      </table>
    </TableScroller>

    <TierPolicyDialog :open="editorOpen" :policy="editing" :tiers="tiers" @close="editorOpen = false" @saved="onSaved" />

    <ConfirmDialog
      :open="!!deleteTarget"
      :title="t('tiering.policy.deleteTitle')"
      :description="tp('tiering.policy.deleteHint', { name: deleteTarget?.name ?? '' })"
      :confirm-label="t('common.delete')"
      :cancel-label="t('common.cancel')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    />
  </CardSection>
</template>

<script setup lang="ts">
import CardSection from './ui/CardSection.vue'
import { computed, reactive, ref } from 'vue'
import { PlusIcon } from '@heroicons/vue/20/solid'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import FormatBadge from './FormatBadge.vue'
import TierPolicyDialog from './TierPolicyDialog.vue'
import { editDeleteActions } from '../lib/rowActions'
import RowActions from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import { deletePolicy, updatePolicy, type StoragePolicy, type StorageTier } from '../api/tiering'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n'
import { formFromPolicy, formToInput, policyConditions } from '../lib/tiering'

defineProps<{ policies: StoragePolicy[]; tiers: StorageTier[] }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const toasts = useToastStore()

const editorOpen = ref(false)
const editing = ref<StoragePolicy | null>(null)
const deleteTarget = ref<StoragePolicy | null>(null)
const deleting = ref(false)
const busy = reactive(new Set<string>())

const policyActions = computed(() => editDeleteActions(t('common.edit'), t('common.delete')))

function onAction(key: string, policy: StoragePolicy): void {
  if (key === 'edit') openEditor(policy)
  else if (key === 'delete') deleteTarget.value = policy
}

function openEditor(policy: StoragePolicy | null): void {
  editing.value = policy
  editorOpen.value = true
}

function onSaved(): void {
  editorOpen.value = false
  emit('changed')
}

/** PUT replaces the whole policy, so the draft is rebuilt from the stored one with only `enabled` flipped. */
async function setEnabled(policy: StoragePolicy, enabled: boolean): Promise<void> {
  const id = policy.id
  if (!id) return
  busy.add(id)
  try {
    await updatePolicy(id, { ...formToInput(formFromPolicy(policy)), enabled })
    emit('changed')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tiering.policy.saveFailed'))
  } finally {
    busy.delete(id)
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target?.id) return
  deleting.value = true
  try {
    await deletePolicy(target.id)
    toasts.success(`${t('tiering.policy.deleted')}: ${target.name}`)
    deleteTarget.value = null
    emit('changed')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tiering.policy.deleteFailed'))
  } finally {
    deleting.value = false
  }
}
</script>
