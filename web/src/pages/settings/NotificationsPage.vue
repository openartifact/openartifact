<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header card -->
    <div class="bg-surface rounded-xl border border-border p-6">
      <div class="text-lg font-bold tracking-tight">{{ t('settings.notifications') }}</div>
      <div class="text-sm text-muted mt-0.5">{{ t('settings.notificationsDesc') }}</div>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="bg-surface rounded-xl border border-border px-5 py-8 flex items-center justify-center">
      <div class="text-sm text-muted">{{ t('common.loading') }}</div>
    </div>

    <!-- Unsupported -->
    <EmptyState
      v-else-if="!supported"
      :title="t('common.empty')"
      :description="t('settings.notifications.unsupported')"
    />

    <!-- Toggle rows -->
    <div v-else class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-3.5 border-b border-border">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('settings.notificationChannels') }}</h3>
      </div>
      <div class="divide-y divide-border">
        <label
          v-for="ch in channels"
          :key="ch.key"
          class="flex items-center justify-between px-5 py-4 gap-4 cursor-pointer"
        >
          <div>
            <div class="text-sm text-foreground font-medium">{{ ch.label }}</div>
          </div>
          <!-- Toggle switch -->
          <button
            type="button"
            role="switch"
            :aria-checked="ch.enabled"
            class="relative inline-flex h-5 w-9 shrink-0 rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            :class="ch.enabled ? 'bg-primary' : 'bg-surface-hover'"
            @click="ch.enabled = !ch.enabled; save()"
          >
            <span
              class="pointer-events-none inline-block h-4 w-4 rounded-full bg-white shadow-sm transition-transform"
              :class="ch.enabled ? 'translate-x-4' : 'translate-x-0'"
            />
          </button>
        </label>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { request, ApiError } from '../../api/client'
import EmptyState from '../../components/EmptyState.vue'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

interface Channel {
  key: string
  label: string
  enabled: boolean
}

const supported = ref(false)
const loading = ref(false)
const channels = ref<Channel[]>([])
const toasts = useToastStore()

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await request<{ channels: Channel[] }>('/api/v1/auth/notifications')
    channels.value = res?.channels ?? []
    supported.value = true
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 501)) {
      supported.value = false
    } else {
      toasts.error(err instanceof Error ? err.message : t('settings.notifications.loadFailed'))
    }
  } finally {
    loading.value = false
  }
}

async function save(): Promise<void> {
  try {
    await request<void>('/api/v1/auth/notifications', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ channels: channels.value }),
    })
    toasts.success(t('common.saved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('settings.notifications.saveFailed'))
  }
}

onMounted(load)
</script>
