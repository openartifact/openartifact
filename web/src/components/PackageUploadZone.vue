<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div
    class="border-2 border-dashed rounded-lg p-6 transition-colors"
    :class="dragOver ? 'border-primary bg-primary-soft/30' : 'border-border bg-surface'"
    @dragenter.prevent="dragOver = true"
    @dragover.prevent="dragOver = true"
    @dragleave.prevent="dragOver = false"
    @drop.prevent="onDrop"
  >
    <div class="flex flex-col items-center text-center">
      <ArrowUpTrayIcon class="w-8 h-8 text-muted mb-2" aria-hidden="true" />
      <p class="text-sm text-foreground">{{ t('upload.dropzone') }}</p>
      <p class="text-xs text-muted mt-1">{{ formatHint }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="fileInput?.click()">
        {{ t('upload.browse') }}
      </AppButton>
      <input
        ref="fileInput"
        type="file"
        multiple
        class="hidden"
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
import { ArrowUpTrayIcon } from '@heroicons/vue/24/outline'
import { useUpload, type UploadEntry, type UploadStatus } from '../composables/useUpload'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ repo: string; format?: string }>()
const emit = defineEmits<{ uploaded: [] }>()

const dragOver = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const upload = useUpload()
const { entries } = upload
const toasts = useToastStore()

// Leaving the page aborts running uploads instead of letting them report into a dead component.
onBeforeUnmount(() => upload.cancelAll())

const formatHint = computed(() => {
  switch (props.format) {
    case 'deb':
      return t('upload.hint.deb')
    case 'rpm':
      return t('upload.hint.rpm')
    case 'maven':
      return t('upload.hint.maven')
    case 'npm':
      return t('upload.hint.npm')
    default:
      return t('upload.hint.generic')
  }
})

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
  for (const f of Array.from(target.files)) startFor(f)
  target.value = ''
}

function onDrop(ev: DragEvent): void {
  dragOver.value = false
  const files = ev.dataTransfer?.files
  if (!files) return
  for (const f of Array.from(files)) startFor(f)
}

async function startFor(file: File): Promise<void> {
  const entry = upload.add(file)
  const url = `/api/v1/repositories/${encodeURIComponent(props.repo)}/packages${
    props.format ? `?format=${encodeURIComponent(props.format)}` : ''
  }`
  try {
    await upload.start(entry, { url })
    toasts.success(t('upload.complete'), file.name)
    emit('uploaded')
  } catch (err) {
    if (entry.status !== 'cancelled') {
      const msg = err instanceof Error ? err.message : t('upload.failed')
      toasts.error(t('upload.failed'), `${file.name}: ${msg}`)
    }
  }
}
</script>
