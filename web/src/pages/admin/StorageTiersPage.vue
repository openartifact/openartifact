<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <PageHeader :title="t('tiering.title')" :description="t('tiering.description')">
      <template #actions>
        <AppButton variant="secondary" size="sm" :loading="refreshing" @click="reload">
          <ArrowPathIcon v-if="!refreshing" class="w-4 h-4" aria-hidden="true" />
          {{ t('common.refresh') }}
        </AppButton>
      </template>
    </PageHeader>

    <div v-if="loading" class="bg-surface rounded-xl border border-border p-5 space-y-3" data-testid="tiering-loading">
      <div class="h-4 w-40 rounded skel" />
      <div v-for="i in 3" :key="i" class="h-8 rounded skel" />
    </div>

    <div
      v-else-if="loadError && !loaded"
      class="bg-surface rounded-xl border border-danger/30 p-5 flex items-center gap-3 text-sm text-danger"
      role="alert"
      data-testid="tiering-error"
    >
      <span class="mr-auto">{{ loadError }}</span>
      <AppButton variant="secondary" size="sm" @click="reload">{{ t('common.refresh') }}</AppButton>
    </div>

    <template v-else>
      <TierPlacementTable :tiers="tiers" @changed="reload" />
      <TierMoverStatus ref="mover" @pass-finished="reload" />
      <TierPolicyTable :policies="policies" :tiers="tiers" @changed="reload" />
      <TierSimulatePanel />
    </template>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import AppButton from '../../components/AppButton.vue'
import PageHeader from '../../components/PageHeader.vue'
import TierMoverStatus from '../../components/TierMoverStatus.vue'
import TierPlacementTable from '../../components/TierPlacementTable.vue'
import TierPolicyTable from '../../components/TierPolicyTable.vue'
import TierSimulatePanel from '../../components/TierSimulatePanel.vue'
import { listPolicies, listTiers, type StoragePolicy, type StorageTier } from '../../api/tiering'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const tiers = ref<StorageTier[]>([])
const policies = ref<StoragePolicy[]>([])
const loading = ref(true)
const refreshing = ref(false)
const loadError = ref('')
// After the first successful load a failed reload only toasts, keeping the screen (and the mover's poll) alive.
const loaded = ref(false)
const toasts = useToastStore()

let latest = 0
const mover = ref<InstanceType<typeof TierMoverStatus> | null>(null)

/** Loads tiers and policies together; later reloads keep the current content on screen. */
async function reload(): Promise<void> {
  const id = ++latest
  refreshing.value = true
  void mover.value?.refresh()
  try {
    const [tierList, policyList] = await Promise.all([listTiers(), listPolicies()])
    if (id !== latest) return
    tiers.value = tierList.items
    policies.value = policyList.items
    loadError.value = ''
    loaded.value = true
  } catch (err) {
    if (id !== latest) return
    const message = err instanceof Error ? err.message : t('tiering.loadFailed')
    if (loaded.value) toasts.error(message)
    else loadError.value = message
  } finally {
    if (id === latest) {
      loading.value = false
      refreshing.value = false
    }
  }
}

onMounted(reload)
</script>
