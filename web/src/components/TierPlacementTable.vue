<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <CardSection :title="t('tiering.placement.title')" :description="t('tiering.placement.description')">
    <template #action>
      <AppButton size="sm" @click="addOpen = true">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('tiering.tier.addTitle') }}
      </AppButton>
    </template>

    <div v-if="tiers.length === 0" class="py-12 text-center">
      <p class="text-sm font-medium text-foreground">{{ t('tiering.placement.empty') }}</p>
      <p class="text-xs text-muted mt-1">{{ t('tiering.placement.emptyHint') }}</p>
    </div>

    <TableScroller v-else>
      <table class="w-full" data-testid="tier-table">
        <thead>
          <tr class="bg-surface-hover/30 text-left text-caption font-semibold text-muted">
            <th scope="col" class="px-5 py-2.5">{{ t('tiering.tier.column.name') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.tier.column.backend') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.tier.column.status') }}</th>
            <th scope="col" data-drop-order="1" class="px-3 py-2.5 text-right">{{ t('tiering.tier.column.rank') }}</th>
            <th scope="col" class="px-3 py-2.5">{{ t('tiering.tier.column.promote') }}</th>
            <th scope="col" data-drop-order="2" class="px-3 py-2.5 text-right">{{ t('tiering.tier.column.blobs') }}</th>
            <th scope="col" data-drop-order="3" class="px-3 py-2.5 min-w-40">{{ t('tiering.tier.column.size') }}</th>
            <th scope="col" class="px-5 py-2.5 text-right" :class="ACTIONS_HEAD">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="tier in tiers" :key="tier.id ?? tier.name" class="group/row" data-testid="tier-row">
            <td class="px-5 py-3" :class="GROW_CELL">
              <div class="flex items-center gap-2">
                <span class="truncate font-mono text-sm font-medium text-foreground" :title="tier.name">{{ tier.name }}</span>
                <StatusBadge v-if="tier.primary" class="shrink-0" :label="t('tiering.tier.primary')" variant="neutral" />
              </div>
            </td>
            <td class="px-3 py-3 text-sm">
              <span class="font-mono text-xs text-foreground">{{ tier.backend ?? t('tiering.tier.primaryBackend') }}</span>
              <span class="text-caption text-muted ml-1.5">{{ tier.backend_kind }}</span>
            </td>
            <td class="px-3 py-3">
              <StatusBadge :label="statusLabel(tier)" :variant="statusVariant(tier)" />
            </td>
            <td class="px-3 py-3 text-right tabular-nums text-sm">{{ tier.rank }}</td>
            <td class="px-3 py-3">
              <AppToggle
                :model-value="!!tier.promote_on_read"
                :label="`${t('tiering.tier.column.promote')}: ${tier.name}`"
                :disabled="tier.primary || busy.has(tier.name ?? '')"
                @update:model-value="setPromote(tier, $event)"
              />
            </td>
            <td class="px-3 py-3 text-right tabular-nums text-sm">{{ formatCount(tier.blob_count) }}</td>
            <td class="px-3 py-3">
              <div class="flex items-center gap-2">
                <div class="h-2 flex-1 rounded-full bg-surface-hover overflow-hidden" aria-hidden="true">
                  <div class="h-full bg-primary" :style="{ width: `${share(tier)}%` }" />
                </div>
                <span class="w-20 text-right text-xs tabular-nums text-muted">{{ formatBytes(tier.total_bytes ?? 0) }}</span>
              </div>
            </td>
            <td class="px-5 py-3" :class="ACTIONS_CELL">
              <RowActions :actions="tierActions(tier)" :subject="tier.name" @action="deleteTarget = tier" />
            </td>
          </tr>
        </tbody>
      </table>
    </TableScroller>

    <TierAddDialog
      :open="addOpen"
      :used-backends="usedBackends"
      @close="addOpen = false"
      @created="onCreated"
    />

    <ConfirmDialog
      :open="!!deleteTarget"
      :title="t('tiering.tier.deleteTitle')"
      :description="tp('tiering.tier.deleteHint', { name: deleteTarget?.name ?? '' })"
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
import { PlusIcon, TrashIcon } from '@heroicons/vue/20/solid'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import StatusBadge from './StatusBadge.vue'
import TierAddDialog from './TierAddDialog.vue'
import RowActions, { type RowAction } from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import { deleteTier, updateTier, type StorageTier } from '../api/tiering'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n'
import { formatBytes } from '../lib/quota'
import { formatCount, sharePercent } from '../lib/tiering'

const props = defineProps<{ tiers: StorageTier[] }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const toasts = useToastStore()

const addOpen = ref(false)
const deleteTarget = ref<StorageTier | null>(null)
const deleting = ref(false)
const busy = reactive(new Set<string>())

const totalBytes = computed(() => props.tiers.reduce((sum, tier) => sum + (tier.total_bytes ?? 0), 0))
const usedBackends = computed(() => props.tiers.flatMap((tier) => (tier.backend ? [tier.backend] : [])))

function share(tier: StorageTier): number {
  return sharePercent(tier.total_bytes, totalBytes.value)
}

function statusLabel(tier: StorageTier): string {
  if (!tier.available) return t('tiering.tier.status.unavailable')
  return tier.backend_status === 'unreachable' ? t('tiering.tier.status.unreachable') : t('tiering.tier.status.available')
}

function statusVariant(tier: StorageTier): 'success' | 'danger' | 'neutral' {
  if (!tier.available || tier.backend_status === 'unreachable') return 'danger'
  return tier.backend_status === 'healthy' || tier.primary ? 'success' : 'neutral'
}

function tierActions(tier: StorageTier): RowAction[] {
  return [
    {
      key: 'delete',
      label: tier.primary ? t('tiering.tier.primaryNoDelete') : t('common.delete'),
      icon: TrashIcon,
      tone: 'danger',
      disabled: tier.primary,
    },
  ]
}

function onCreated(): void {
  addOpen.value = false
  emit('changed')
}

async function setPromote(tier: StorageTier, value: boolean): Promise<void> {
  const name = tier.name ?? ''
  busy.add(name)
  try {
    await updateTier(name, { promote_on_read: value })
    emit('changed')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tiering.tier.saveFailed'))
  } finally {
    busy.delete(name)
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target?.name) return
  deleting.value = true
  try {
    await deleteTier(target.name)
    toasts.success(`${t('tiering.tier.deleted')}: ${target.name}`)
    deleteTarget.value = null
    emit('changed')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tiering.tier.deleteFailed'))
  } finally {
    deleting.value = false
  }
}
</script>
