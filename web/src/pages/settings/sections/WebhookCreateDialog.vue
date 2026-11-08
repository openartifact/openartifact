<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="open"
    drawer
    :eyebrow="t('adm.webhooks.eyebrow')"
    :title="t('adm.webhooks.addTitle')"
    @close="emit('close')"
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
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="webhook-form" size="sm" :loading="saving" :disabled="!canSave">
        {{ t('common.save') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { createWebhook, type WebhookCreateInput } from '../../../api/webhooks'
import AdminDialog from '../../../components/AdminDialog.vue'
import AppButton from '../../../components/AppButton.vue'
import AppToggle from '../../../components/AppToggle.vue'
import { useToastStore } from '../../../stores/toast'
import { t, tDynamic } from '../../../i18n'
import {
  WEBHOOK_EVENTS,
  WEBHOOK_PAYLOAD_FORMATS,
  type WebhookEvent,
  type WebhookPayloadFormat,
} from '../../../lib/webhookEvents'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; created: [] }>()

const toasts = useToastStore()
const saving = ref(false)

const emptyForm = () => ({
  name: '',
  url: '',
  secret: '',
  events: [] as WebhookEvent[],
  payload_format: 'openartifact' as WebhookPayloadFormat,
  ssl_verify: true,
  enabled: true,
})
const form = ref(emptyForm())

watch(
  () => props.open,
  (open) => {
    if (open) form.value = emptyForm()
  },
)

const eventOptions = WEBHOOK_EVENTS.map((value) => ({
  value,
  hint: tDynamic(`adm.webhooks.event.${value}`),
}))

const canSave = computed(
  () => !saving.value && !!form.value.name.trim() && !!form.value.url.trim() && form.value.events.length > 0,
)

function toggleEvent(value: WebhookEvent): void {
  const idx = form.value.events.indexOf(value)
  if (idx === -1) {
    form.value.events.push(value)
  } else {
    form.value.events.splice(idx, 1)
  }
}

async function submitCreate(): Promise<void> {
  if (!canSave.value) return
  saving.value = true
  try {
    const payload: WebhookCreateInput = {
      name: form.value.name.trim(),
      url: form.value.url.trim(),
      events: form.value.events,
      payload_format: form.value.payload_format,
      ssl_verify: form.value.ssl_verify,
      enabled: form.value.enabled,
    }
    if (form.value.secret.trim()) payload.secret = form.value.secret.trim()

    await createWebhook(payload)
    toasts.success(t('adm.webhooks.created'))
    emit('created')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.webhooks.createFailed'))
  } finally {
    saving.value = false
  }
}
</script>
