<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('adm.webhooks.title')" :description="t('adm.webhooks.description')">
      <template #actions>
        <div class="flex items-center gap-2">
          <AppButton size="sm" @click="openCreate">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.webhooks.add') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <!-- Stat tiles (derived from the loaded list only) -->
    <div class="grid grid-cols-2 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-primary bg-primary-soft">
          <BellAlertIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-micro font-semibold text-muted">{{ t('adm.webhooks.stat.active') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ activeCount }}</div>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-muted bg-surface-hover">
          <PaperAirplaneIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-micro font-semibold text-muted">{{ t('adm.webhooks.stat.disabled') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ webhooks.length - activeCount }}</div>
        </div>
      </div>
    </div>

    <!-- Table card -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.webhooks.listTitle') }}</h3>
        <span class="text-caption font-mono text-muted">{{ webhooks.length }} {{ t('adm.common.total') }}</span>
      </div>

      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>

      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>

      <div v-else-if="webhooks.length === 0" class="bg-surface rounded-xl border-0">
        <EmptyState
          :title="t('adm.webhooks.empty')"
          :description="t('adm.webhooks.emptyHint')"
          :docs-url="docsUrl"
          :docs-label="t('docs.webhooks')"
        />
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-caption font-semibold text-muted bg-surface-hover/30">
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.name') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.url') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.events') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.format') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.status') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.created') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="hook in webhooks" :key="hook.id" class="hover:bg-surface-hover/40 transition-colors">
              <td class="px-4 py-3">
                <span class="font-mono text-ui font-medium">{{ hook.name || '—' }}</span>
              </td>
              <td class="px-4 py-3 max-w-[200px]">
                <span class="text-xs text-muted font-mono truncate block" :title="hook.url">{{ hook.url || '—' }}</span>
              </td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-1.5 flex-wrap">
                  <span
                    v-for="event in hook.events || []"
                    :key="event"
                    class="inline-flex items-center px-2 py-0.5 rounded-full text-micro font-medium bg-surface-hover text-muted border border-border"
                  >
                    {{ event }}
                  </span>
                  <span v-if="!hook.events?.length" class="text-xs text-muted">—</span>
                </div>
              </td>
              <td class="px-4 py-3 text-xs text-muted">{{ formatLabel(hook.payload_format) }}</td>
              <td class="px-4 py-3">
                <span
                  v-if="hook.enabled"
                  class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium bg-success-soft/40 text-success"
                >
                  <span class="w-1.5 h-1.5 rounded-full bg-success" />
                  {{ t('adm.webhooks.statusActive') }}
                </span>
                <span v-else class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium bg-danger-soft/40 text-danger">
                  <span class="w-1.5 h-1.5 rounded-full bg-danger" />
                  {{ t('adm.webhooks.statusInactive') }}
                </span>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDate(hook.created_at) }}</td>
              <td class="px-4 py-3">
                <button
                  type="button"
                  @click="deleteTarget = hook"
                  :title="t('common.delete')"
                  :aria-label="`${t('common.delete')} ${hook.name || hook.url || ''}`"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Create Drawer -->
    <AdminDialog
      :open="drawerOpen"
      drawer
      :eyebrow="t('adm.webhooks.eyebrow')"
      :title="t('adm.webhooks.addTitle')"
      @close="closeDrawer"
    >
      <form id="webhook-form" class="space-y-5" @submit.prevent="submitCreate">
        <div>
          <label for="wh-name" class="block text-sm font-medium text-foreground mb-1.5">
            {{ t('adm.webhooks.field.name') }} <span class="text-danger">*</span>
          </label>
          <input
            id="wh-name"
            v-model="form.name"
            type="text"
            required
            :placeholder="t('adm.webhooks.field.namePlaceholder')"
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
          />
        </div>

        <div>
          <label for="wh-url" class="block text-sm font-medium text-foreground mb-1.5">
            {{ t('adm.webhooks.field.url') }} <span class="text-danger">*</span>
          </label>
          <input
            id="wh-url"
            v-model="form.url"
            type="url"
            required
            placeholder="https://example.com/webhook"
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
          />
        </div>

        <div>
          <label for="wh-secret" class="block text-sm font-medium text-foreground mb-1.5">
            {{ t('adm.webhooks.field.secret') }} <span class="text-muted font-normal">({{ t('common.optional') }})</span>
          </label>
          <input
            id="wh-secret"
            v-model="form.secret"
            type="password"
            autocomplete="new-password"
            :placeholder="t('adm.webhooks.field.secretPlaceholder')"
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
          />
        </div>

        <div>
          <label for="wh-format" class="block text-sm font-medium text-foreground mb-1.5">
            {{ t('adm.webhooks.field.payloadFormat') }}
          </label>
          <select
            id="wh-format"
            v-model="form.payload_format"
            aria-describedby="wh-format-hint"
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 text-foreground focus:ring-2 focus:ring-primary/50"
          >
            <option v-for="format in WEBHOOK_PAYLOAD_FORMATS" :key="format" :value="format">
              {{ tDynamic(`adm.webhooks.format.${format}`) }}
            </option>
          </select>
          <p id="wh-format-hint" class="text-caption text-muted mt-1">{{ t('adm.webhooks.field.payloadFormatHint') }}</p>
        </div>

        <fieldset>
          <legend class="block text-sm font-medium text-foreground mb-2">
            {{ t('adm.webhooks.field.events') }} <span class="text-danger">*</span>
          </legend>
          <div class="space-y-2">
            <label
              v-for="event in eventOptions"
              :key="event.value"
              class="flex items-center gap-2.5 cursor-pointer group"
            >
              <input
                :checked="form.events.includes(event.value)"
                type="checkbox"
                class="rounded border-border-strong text-primary focus:ring-primary/50"
                @change="toggleEvent(event.value)"
              />
              <div>
                <span class="text-sm text-foreground font-mono">{{ event.value }}</span>
                <p class="text-caption text-muted">{{ event.hint }}</p>
              </div>
            </label>
          </div>
        </fieldset>

        <div class="flex items-center justify-between py-1.5">
          <div>
            <p id="wh-ssl-label" class="text-sm font-medium text-foreground">{{ t('adm.webhooks.field.sslVerify') }}</p>
            <p class="text-caption text-muted">{{ t('adm.webhooks.field.sslVerifyHint') }}</p>
          </div>
          <AppToggle v-model="form.ssl_verify" labelledby="wh-ssl-label" />
        </div>

        <div class="flex items-center justify-between py-1.5">
          <div>
            <p id="wh-active-label" class="text-sm font-medium text-foreground">{{ t('adm.webhooks.field.active') }}</p>
            <p class="text-caption text-muted">{{ t('adm.webhooks.field.activeHint') }}</p>
          </div>
          <AppToggle v-model="form.enabled" labelledby="wh-active-label" />
        </div>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="closeDrawer">{{ t('common.cancel') }}</AppButton>
        <AppButton type="submit" form="webhook-form" size="sm" :loading="saving" :disabled="!canSave">
          {{ t('common.save') }}
        </AppButton>
      </template>
    </AdminDialog>

    <ConfirmDialog
      :open="!!deleteTarget"
      :title="t('adm.webhooks.confirmDelete')"
      :description="deleteTarget ? deleteTarget.name || deleteTarget.url : ''"
      :confirm-label="t('common.delete')"
      :cancel-label="t('common.cancel')"
      :busy="deleting"
      @confirm="deleteOne"
      @close="deleteTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../../components/PageHeader.vue'
import { formatDate } from '../../lib/datetime'
import { ref, computed, onMounted } from 'vue'
import { request } from '../../api/client'
import { useDocsLink } from '../../composables/useDocsLink'
import { useToastStore } from '../../stores/toast'
import { t, tDynamic } from '../../i18n/en'
import {
  PlusIcon,
  BellAlertIcon,
  PaperAirplaneIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import AppButton from '../../components/AppButton.vue'
import AppToggle from '../../components/AppToggle.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import {
  WEBHOOK_EVENTS,
  WEBHOOK_PAYLOAD_FORMATS,
  type WebhookEvent,
  type WebhookPayloadFormat,
} from '../../lib/webhookEvents'

interface Webhook {
  id: string
  repository_id?: string
  name: string
  url: string
  has_secret?: boolean
  events: string[]
  content_type?: string
  payload_format?: string
  ssl_verify?: boolean
  enabled: boolean
  max_retries?: number
  timeout_seconds?: number
  created_at?: string
  updated_at?: string
}

interface WebhookListResponse {
  webhooks?: Webhook[]
}

const toasts = useToastStore()
const docsUrl = useDocsLink('admin-webhooks')

const webhooks = ref<Webhook[]>([])
const loading = ref(false)
const loadError = ref('')
const drawerOpen = ref(false)
const saving = ref(false)
const deleteTarget = ref<Webhook | null>(null)
const deleting = ref(false)

const form = ref({
  name: '',
  url: '',
  secret: '',
  events: [] as WebhookEvent[],
  payload_format: 'openartifact' as WebhookPayloadFormat,
  ssl_verify: true,
  enabled: true,
})

const eventOptions = WEBHOOK_EVENTS.map((value) => ({
  value,
  hint: tDynamic(`adm.webhooks.event.${value}`),
}))

const activeCount = computed(() => webhooks.value.filter((h) => h.enabled).length)
const canSave = computed(
  () => !saving.value && !!form.value.name.trim() && !!form.value.url.trim() && form.value.events.length > 0,
)


function formatLabel(format?: string): string {
  return tDynamic(`adm.webhooks.format.${format || 'openartifact'}`)
}

function toggleEvent(value: WebhookEvent): void {
  const idx = form.value.events.indexOf(value)
  if (idx === -1) {
    form.value.events.push(value)
  } else {
    form.value.events.splice(idx, 1)
  }
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await request<WebhookListResponse>('/api/v1/webhooks')
    webhooks.value = res?.webhooks ?? []
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.webhooks.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  form.value = {
    name: '',
    url: '',
    secret: '',
    events: [],
    payload_format: 'openartifact',
    ssl_verify: true,
    enabled: true,
  }
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
}

async function submitCreate(): Promise<void> {
  if (!canSave.value) return
  saving.value = true
  try {
    const payload: Record<string, unknown> = {
      name: form.value.name.trim(),
      url: form.value.url.trim(),
      events: form.value.events,
      payload_format: form.value.payload_format,
      ssl_verify: form.value.ssl_verify,
    }
    if (form.value.secret.trim()) payload.secret = form.value.secret.trim()

    const created = await request<Webhook>('/api/v1/webhooks', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })

    // The create endpoint always enables the hook with TLS verification on;
    // apply the requested non-default flags with a follow-up update.
    if (created?.id && (!form.value.enabled || !form.value.ssl_verify)) {
      await request<Webhook>(`/api/v1/webhooks/${encodeURIComponent(created.id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: form.value.enabled, ssl_verify: form.value.ssl_verify }),
      })
    }
    toasts.success(t('adm.webhooks.created'))
    closeDrawer()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.webhooks.createFailed'))
  } finally {
    saving.value = false
  }
}

async function deleteOne(): Promise<void> {
  const hook = deleteTarget.value
  if (!hook?.id) return
  deleting.value = true
  try {
    await request(`/api/v1/webhooks/${encodeURIComponent(hook.id)}`, { method: 'DELETE' })
    toasts.success(t('adm.webhooks.deleted'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.webhooks.deleteFailed'))
  } finally {
    deleting.value = false
    deleteTarget.value = null
  }
}

onMounted(load)
</script>
