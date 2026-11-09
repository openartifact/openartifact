<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Add or edit a vendor CSAF VEX feed; saving replaces every editable field. -->
<template>
  <AdminDialog :open="open" drawer :eyebrow="t('sx.vexFeeds.eyebrow')" :title="feed ? t('sx.vexFeeds.editTitle') : t('sx.vexFeeds.addTitle')" @close="close">
    <form id="vex-feed-form" class="space-y-4" novalidate data-testid="vex-feed-form" @submit.prevent="save">
      <FormField id="vex-feed-name" :label="t('sx.vexFeeds.field.name')" :hint="t('sx.vexFeeds.field.nameHint')" :error="errorText('name')">
        <input id="vex-feed-name" v-model="form.name" type="text" autocomplete="off" required :class="INPUT_CLASS" data-testid="vex-feed-name" />
      </FormField>
      <FormField id="vex-feed-url" :label="t('sx.vexFeeds.field.url')" :hint="t('sx.vexFeeds.field.urlHint')" :error="errorText('url')">
        <input id="vex-feed-url" v-model="form.url" type="url" autocomplete="off" required placeholder="https://" :class="MONO_INPUT_CLASS" data-testid="vex-feed-url" />
      </FormField>
      <FormField id="vex-feed-interval" :label="t('sx.vexFeeds.field.interval')" :hint="intervalHint" :error="errorText('intervalHours')">
        <input
          id="vex-feed-interval"
          v-model.number="form.intervalHours"
          type="number"
          :min="VEX_FEED_INTERVAL_MIN"
          :max="VEX_FEED_INTERVAL_MAX"
          step="1"
          required
          :class="INPUT_CLASS"
          data-testid="vex-feed-interval"
        />
      </FormField>
      <FormField id="vex-feed-keys" :label="t('sx.vexFeeds.field.keys')" :hint="t('sx.vexFeeds.field.keysHint')" :error="errorText('pgpKeys')">
        <textarea id="vex-feed-keys" v-model="form.pgpKeys" rows="5" spellcheck="false" :class="[MONO_INPUT_CLASS, 'text-xs']" data-testid="vex-feed-keys" />
      </FormField>

      <div class="flex items-center justify-between gap-4 py-1.5">
        <div>
          <p id="vex-feed-signature-label" class="text-sm font-medium text-foreground">{{ t('sx.vexFeeds.field.requireSignature') }}</p>
          <p class="text-caption text-muted">{{ t('sx.vexFeeds.field.requireSignatureHint') }}</p>
        </div>
        <AppToggle v-model="form.requireSignature" labelledby="vex-feed-signature-label" />
      </div>
      <div class="flex items-center justify-between gap-4 py-1.5">
        <div>
          <p id="vex-feed-enabled-label" class="text-sm font-medium text-foreground">{{ t('sx.vexFeeds.field.enabled') }}</p>
          <p class="text-caption text-muted">{{ t('sx.vexFeeds.field.enabledHint') }}</p>
        </div>
        <AppToggle v-model="form.enabled" labelledby="vex-feed-enabled-label" />
      </div>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" :disabled="saving" @click="close">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="vex-feed-form" size="sm" :loading="saving" data-testid="vex-feed-save">{{ t('common.save') }}</AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from '../../../components/AdminDialog.vue'
import AppButton from '../../../components/AppButton.vue'
import AppToggle from '../../../components/AppToggle.vue'
import FormField from '../../../components/FormField.vue'
import { createVexFeed, updateVexFeed, type VexFeed } from '../../../api/vexFeeds'
import { INPUT_CLASS, MONO_INPUT_CLASS } from '../../../lib/formClasses'
import {
  emptyVexFeedForm,
  validateVexFeedForm,
  vexFeedBody,
  vexFeedFormFrom,
  VEX_FEED_INTERVAL_MAX,
  VEX_FEED_INTERVAL_MIN,
  type VexFeedForm,
  type VexFeedFormErrors,
} from '../../../lib/vexFeeds'
import { useToastStore } from '../../../stores/toast'
import { t, tDynamic, tp } from '../../../i18n'

const props = defineProps<{
  open: boolean
  /** The feed being edited; null adds one. */
  feed: VexFeed | null
}>()
const emit = defineEmits<{ close: []; saved: [feed: VexFeed] }>()

const toasts = useToastStore()
const form = ref<VexFeedForm>(emptyVexFeedForm())
// Errors appear once a save was attempted.
const attempted = ref(false)
const saving = ref(false)

const errors = computed<VexFeedFormErrors>(() => validateVexFeedForm(form.value))
const intervalHint = computed(() => tp('sx.vexFeeds.field.intervalHint', { min: String(VEX_FEED_INTERVAL_MIN), max: String(VEX_FEED_INTERVAL_MAX) }))

function errorText(field: keyof VexFeedFormErrors): string | undefined {
  const code = attempted.value ? errors.value[field] : undefined
  if (!code) return undefined
  if (code === 'range') return tp('sx.vexFeeds.error.intervalHours.range', { min: String(VEX_FEED_INTERVAL_MIN), max: String(VEX_FEED_INTERVAL_MAX) })
  return tDynamic(`sx.vexFeeds.error.${field}.${code}`)
}

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    form.value = props.feed ? vexFeedFormFrom(props.feed) : emptyVexFeedForm()
    attempted.value = false
  },
  { immediate: true },
)

function close(): void {
  if (!saving.value) emit('close')
}

async function save(): Promise<void> {
  if (saving.value) return
  attempted.value = true
  if (Object.keys(errors.value).length > 0) return
  saving.value = true
  try {
    const body = vexFeedBody(form.value)
    const saved = props.feed ? await updateVexFeed(props.feed.id, body) : await createVexFeed(body)
    toasts.success(tp('sx.vexFeeds.saved', { name: body.name }))
    emit('saved', saved)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sx.vexFeeds.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
