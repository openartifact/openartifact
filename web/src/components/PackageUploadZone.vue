<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div v-if="publishSnippet" class="rounded-lg border border-border bg-surface p-4">
    <p class="text-sm text-foreground">{{ t('upload.nix.body') }}</p>
    <pre class="mt-3 overflow-x-auto rounded-md border border-border bg-bg p-3 text-xs text-foreground">{{ publishSnippet }}</pre>
  </div>
  <div
    v-else-if="plan"
    class="border-2 border-dashed rounded-lg p-6 transition-colors"
    :class="dragOver ? 'border-primary bg-primary-soft/30' : 'border-border bg-surface'"
    @dragenter.prevent="dragOver = true"
    @dragover.prevent="dragOver = true"
    @dragleave.prevent="dragOver = false"
    @drop.prevent="onDrop"
  >
    <div v-if="visibleFields.length" class="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3" data-testid="upload-fields">
      <FormField
        v-for="f in visibleFields"
        :key="f.key"
        :label="t(f.label)"
        :id="`upload-${f.key}`"
        :hint="f.hint ? t(f.hint) : undefined"
        :error="errorOf(f)"
      >
        <select v-if="f.options" :id="`upload-${f.key}`" v-model="values[f.key]" :class="INPUT_CLASS">
          <option v-for="o in f.options" :key="o.value" :value="o.value">{{ t(o.label) }}</option>
        </select>
        <input
          v-else
          :id="`upload-${f.key}`"
          v-model.trim="values[f.key]"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :placeholder="f.placeholder"
          :aria-invalid="errorOf(f) !== ''"
          :class="INPUT_CLASS"
        />
      </FormField>
    </div>
    <div class="flex flex-col items-center text-center">
      <ArrowUpTrayIcon class="w-8 h-8 text-muted mb-2" aria-hidden="true" />
      <p class="text-sm text-foreground">{{ t('upload.dropzone') }}</p>
      <p class="text-xs text-muted mt-1">{{ t(plan.hint) }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" :disabled="!inputsValid" @click="fileInput?.click()">
        {{ t('upload.browse') }}
      </AppButton>
      <input
        ref="fileInput"
        type="file"
        multiple
        class="hidden"
        :accept="plan.accept"
        @change="onPick"
      />
    </div>

    <ul v-if="entries.length" class="mt-4 space-y-2">
      <li
        v-for="entry in entries"
        :key="entry.id"
        class="bg-bg border border-border rounded-md p-2 flex items-center gap-3"
      >
        <div class="flex-1 min-w-0">
          <p class="text-sm text-foreground truncate">{{ entry.file.name }}</p>
          <div class="h-1 bg-surface-hover rounded mt-1 overflow-hidden">
            <div
              class="h-full bg-primary"
              :style="{ width: `${percent(entry)}%` }"
              :class="{ 'bg-success': entry.status === 'complete', 'bg-danger': entry.status === 'failed' || entry.status === 'cancelled' }"
            />
          </div>
          <p class="text-xs text-muted mt-0.5">
            {{ statusLabel(entry.status) }}{{ entry.error ? ` — ${entry.error}` : '' }}
          </p>
        </div>
        <button
          v-if="entry.status === 'uploading' || entry.status === 'queued'"
          type="button"
          class="text-xs text-danger hover:opacity-80"
          @click="upload.cancel(entry)"
        >
          {{ t('upload.cancel') }}
        </button>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onBeforeUnmount } from 'vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import { ArrowUpTrayIcon } from '@heroicons/vue/24/outline'
import { useUpload, type UploadEntry, type UploadStatus } from '../composables/useUpload'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'
import { clientSetup } from '../lib/installCommands'
import { fieldError, initialValues, uploadMode, uploadPlan, type UploadField } from '../lib/uploadPlans'
import { UploadInputError } from '../lib/uploadInput'

const props = defineProps<{ repo: string; format?: string }>()
const emit = defineEmits<{ uploaded: [] }>()

const dragOver = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const upload = useUpload()
const { entries } = upload
const toasts = useToastStore()

// Leaving the page aborts running uploads instead of letting them report into a dead component.
onBeforeUnmount(() => upload.cancelAll())

// How the format is uploaded (lib/uploadPlans.ts); a nix cache is published with `nix copy`, which a browser cannot do.
const plan = computed(() => uploadPlan(props.format))
const values = ref(plan.value ? initialValues(plan.value) : {})
const publishSnippet = computed(() =>
  uploadMode(props.format) === 'publish' ? (clientSetup(props.format ?? '', window.location.origin, props.repo)[0]?.publish ?? '') : '',
)

const visibleFields = computed(() => (plan.value?.fields ?? []).filter((f) => !f.when || f.when(values.value)))
// A required input nobody has typed in yet (no default) keeps browsing off without scolding; a cleared default does show the message.
function errorOf(field: UploadField): string {
  if (field.value === '' && (values.value[field.key] ?? '').trim() === '') return ''
  const key = fieldError(field, values.value)
  return key ? t(key) : ''
}
// Browsing and dropping stay off until every input that applies is valid.
const inputsValid = computed(() => visibleFields.value.every((f) => fieldError(f, values.value) === ''))

function percent(entry: UploadEntry): number {
  if (entry.status === 'complete') return 100
  if (!entry.total) return 0
  return Math.min(100, Math.round((entry.loaded / entry.total) * 100))
}

function statusLabel(status: UploadStatus): string {
  switch (status) {
    case 'queued':
      return t('upload.queued')
    case 'uploading':
      return t('upload.uploading')
    case 'complete':
      return t('upload.complete')
    case 'failed':
      return t('upload.failed')
    case 'cancelled':
      return t('common.cancel')
  }
}

function onPick(ev: Event): void {
  const target = ev.target as HTMLInputElement
  if (!target.files) return
  if (inputsValid.value) void startAll(Array.from(target.files))
  target.value = ''
}

function onDrop(ev: DragEvent): void {
  dragOver.value = false
  const files = ev.dataTransfer?.files
  if (!files || !inputsValid.value) return
  void startAll(Array.from(files))
}

async function startAll(files: File[]): Promise<void> {
  const order = plan.value?.order
  if (!order) {
    for (const f of files) void startFor(f)
    return
  }
  // One at a time, in the order the format needs (pacman: packages before their signatures).
  for (const f of order(files)) await startFor(f)
}

async function startFor(file: File): Promise<void> {
  const entry = upload.add(file)
  // The inputs as they were when the file was picked, whatever is typed while it uploads.
  const snapshot = { ...values.value }
  try {
    const request = await plan.value!.request(props.repo, file, snapshot)
    // Cancelled while the request was being built (npm reads the tarball first).
    if (entry.status === 'cancelled') return
    await upload.start(entry, request)
    toasts.success(t('upload.complete'), file.name)
    emit('uploaded')
  } catch (err) {
    if (entry.status === 'cancelled') return
    const msg = err instanceof UploadInputError ? t(err.key) : err instanceof Error ? err.message : t('upload.failed')
    if (err instanceof UploadInputError) {
      entry.status = 'failed'
      entry.error = msg
    }
    toasts.error(t('upload.failed'), `${file.name}: ${msg}`)
  }
}
</script>
