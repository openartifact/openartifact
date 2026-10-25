<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('tiering.policy.title')">
    <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
      <div class="min-w-0 mr-auto">
        <h2 class="text-sm font-semibold text-foreground">{{ t('tiering.policy.title') }}</h2>
        <p class="text-caption text-muted">{{ t('tiering.policy.description') }}</p>
      </div>
      <AppButton size="sm" :disabled="tiers.length === 0" @click="openEditor(null)">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('tiering.policy.addTitle') }}
      </AppButton>
    </div>

    <div v-if="policies.length === 0" class="py-12 text-center">
      <p class="text-sm font-medium text-foreground">{{ t('tiering.policy.empty') }}</p>
      <p class="text-xs text-muted mt-1">{{ t('tiering.policy.emptyHint') }}</p>
    </div>

    <div v-else class="overflow-x-auto">
      <table class="w-full" data-testid="policy-table">
        <thead>
          <tr class="bg-surface-hover/30 text-left text-caption font-semibold text-muted">
            <th scope="col" class="px-5 py-2.5 text-right w-16">{{ t('tiering.policy.column.position') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.name') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.enabled') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.conditions') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.policy.column.route') }}</th>
            <th scope="col" class="px-5 py-2.5 text-right">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="p in policies" :key="p.id" data-testid="policy-row">
            <td class="px-5 py-3 text-right tabular-nums text-sm text-muted">{{ p.position }}</td>
            <td class="px-3 py-3 text-sm font-medium text-foreground">{{ p.name }}</td>
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
            <td class="px-3 py-3 font-mono text-xs whitespace-nowrap">
              {{ p.from_tier ?? t('tiering.policy.anyTier') }} → {{ p.target_tier }}
            </td>
            <td class="px-5 py-3 text-right whitespace-nowrap">
              <AppButton variant="ghost" size="xs" :aria-label="`${t('common.edit')} ${p.name}`" @click="openEditor(p)">
                <PencilIcon class="w-4 h-4" aria-hidden="true" />
              </AppButton>
              <AppButton variant="ghost" size="xs" :aria-label="`${t('common.delete')} ${p.name}`" @click="deleteTarget = p">
                <TrashIcon class="w-4 h-4" aria-hidden="true" />
              </AppButton>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

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
  </section>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { PencilIcon, PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import FormatBadge from './FormatBadge.vue'
import TierPolicyDialog from './TierPolicyDialog.vue'
import { deletePolicy, updatePolicy, type StoragePolicy, type StorageTier } from '../api/tiering'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n/en'
import { formFromPolicy, formToInput, policyConditions } from '../lib/tiering'

defineProps<{ policies: StoragePolicy[]; tiers: StorageTier[] }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const toasts = useToastStore()

const editorOpen = ref(false)
const editing = ref<StoragePolicy | null>(null)
const deleteTarget = ref<StoragePolicy | null>(null)
const deleting = ref(false)
const busy = reactive(new Set<string>())

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
