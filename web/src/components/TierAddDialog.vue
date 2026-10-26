<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog :open="open" :title="t('tiering.tier.addTitle')" :description="t('tiering.tier.addHint')" @close="emit('close')">
    <form id="tier-add-form" class="space-y-4" @submit.prevent="submit">
      <div>
        <label for="tier-name" class="block text-xs font-semibold text-foreground mb-1.5">
          {{ t('tiering.tier.field.name') }} <span class="text-danger">*</span>
        </label>
        <input
          id="tier-name"
          v-model="name"
          type="text"
          autocomplete="off"
          :placeholder="t('tiering.tier.field.namePlaceholder')"
          class="w-full h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary"
        />
      </div>

      <div>
        <label for="tier-backend" class="block text-xs font-semibold text-foreground mb-1.5">
          {{ t('tiering.tier.field.backend') }} <span class="text-danger">*</span>
        </label>
        <select
          id="tier-backend"
          v-model="backend"
          :disabled="loadingBackends"
          class="w-full h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary"
        >
          <option value="">{{ loadingBackends ? t('common.loading') : t('tiering.tier.field.backendPlaceholder') }}</option>
          <option v-for="b in freeBackends" :key="b.name" :value="b.name">{{ b.name }} ({{ b.kind }})</option>
        </select>
        <p v-if="loadError" class="text-caption text-danger mt-1" role="alert">{{ loadError }}</p>
        <p v-else-if="!loadingBackends && freeBackends.length === 0" class="text-caption text-muted mt-1">
          {{ t('tiering.tier.noBackends') }}
        </p>
      </div>

      <div>
        <label for="tier-rank" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('tiering.tier.field.rank') }}</label>
        <input
          id="tier-rank"
          v-model.number="rank"
          type="number"
          min="0"
          step="1"
          class="w-32 h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary"
        />
        <p class="text-caption text-muted mt-1">{{ t('tiering.tier.field.rankHint') }}</p>
      </div>

      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p id="tier-promote-label" class="text-xs font-semibold text-foreground">{{ t('tiering.tier.field.promote') }}</p>
          <p class="text-caption text-muted">{{ t('tiering.tier.field.promoteHint') }}</p>
        </div>
        <AppToggle v-model="promote" labelledby="tier-promote-label" />
      </div>
    </form>

    <template #footer>
      <AppButton variant="ghost" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="tier-add-form" :loading="saving" :disabled="!name.trim() || !backend">
        {{ t('tiering.tier.addAction') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import { listStorageBackends, type StorageBackend } from '../api/storage'
import { createTier } from '../api/tiering'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

const props = defineProps<{
  open: boolean
  /** Backend names already mapped to a tier; a backend can back one tier only. */
  usedBackends: string[]
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'created'): void }>()

const toasts = useToastStore()

const backends = ref<StorageBackend[]>([])
const loadingBackends = ref(false)
const loadError = ref('')
const name = ref('')
const backend = ref('')
const rank = ref<number | string>('')
const promote = ref(false)
const saving = ref(false)

const freeBackends = computed(() => backends.value.filter((b) => b.name && !props.usedBackends.includes(b.name)))

async function loadBackends(): Promise<void> {
  loadingBackends.value = true
  loadError.value = ''
  try {
    backends.value = (await listStorageBackends()).items
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('tiering.tier.backendsFailed')
  } finally {
    loadingBackends.value = false
  }
}

watch(
  () => props.open,
  (open) => {
    if (!open) return
    name.value = ''
    backend.value = ''
    rank.value = ''
    promote.value = false
    void loadBackends()
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  if (saving.value || !name.value.trim() || !backend.value) return
  saving.value = true
  try {
    const hasRank = rank.value !== '' && Number.isFinite(Number(rank.value))
    await createTier({
      name: name.value.trim(),
      backend: backend.value,
      ...(hasRank ? { rank: Number(rank.value) } : {}),
      promote_on_read: promote.value,
    })
    toasts.success(`${t('tiering.tier.created')}: ${name.value.trim()}`)
    emit('created')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tiering.tier.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
