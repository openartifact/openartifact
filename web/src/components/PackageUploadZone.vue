<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div v-if="format === 'terraform'" class="rounded-lg border border-border bg-surface p-4">
    <p class="text-sm text-foreground">{{ t('upload.terraform.body') }}</p>
    <pre class="mt-3 overflow-x-auto rounded-md border border-border bg-bg p-3 text-xs text-foreground">{{ terraformPublish }}</pre>
  </div>
  <div
    v-else
    class="border-2 border-dashed rounded-lg p-6 transition-colors"
    :class="dragOver ? 'border-primary bg-primary-soft/30' : 'border-border bg-surface'"
    @dragenter.prevent="dragOver = true"
    @dragover.prevent="dragOver = true"
    @dragleave.prevent="dragOver = false"
    @drop.prevent="onDrop"
  >
    <div v-if="isApk" class="mb-4 grid gap-3 sm:grid-cols-3" data-testid="apk-coordinates">
      <FormField :label="t('upload.apk.branch')" id="apk-upload-branch" :hint="t('upload.apk.branchHint')" :error="branchError">
        <input id="apk-upload-branch" v-model.trim="apkBranch" type="text" autocomplete="off" :aria-invalid="branchError !== ''" :class="INPUT_CLASS" />
      </FormField>
      <FormField :label="t('upload.apk.repo')" id="apk-upload-repo" :hint="t('upload.apk.repoHint')" :error="repoError">
        <input id="apk-upload-repo" v-model.trim="apkRepo" type="text" autocomplete="off" :aria-invalid="repoError !== ''" :class="INPUT_CLASS" />
      </FormField>
      <FormField :label="t('upload.apk.arch')" id="apk-upload-arch" :hint="t('upload.apk.archHint')" :error="archError">
        <input id="apk-upload-arch" v-model.trim="apkArch" type="text" autocomplete="off" placeholder="x86_64" :aria-invalid="archError !== ''" :class="INPUT_CLASS" />
      </FormField>
    </div>
    <div class="flex flex-col items-center text-center">
      <ArrowUpTrayIcon class="w-8 h-8 text-muted mb-2" aria-hidden="true" />
      <p class="text-sm text-foreground">{{ t('upload.dropzone') }}</p>
      <p class="text-xs text-muted mt-1">{{ formatHint }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" :disabled="!coordinatesValid" @click="fileInput?.click()">
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
import FormField from './FormField.vue'
import { ArrowUpTrayIcon } from '@heroicons/vue/24/outline'
import { useUpload, type UploadEntry, type UploadStatus } from '../composables/useUpload'
import { APK_DEFAULT_BRANCH, APK_DEFAULT_REPO, validApkSegment } from '../lib/apk'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'
import { clientSetup } from '../lib/installCommands'

const props = defineProps<{ repo: string; format?: string }>()
const emit = defineEmits<{ uploaded: [] }>()

const dragOver = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const upload = useUpload()
const { entries } = upload
const toasts = useToastStore()

// Leaving the page aborts running uploads instead of letting them report into a dead component.
onBeforeUnmount(() => upload.cancelAll())

// An apk upload needs the mirror-tree directory (`<branch>/<repo>/`); the architecture comes from the package.
const isApk = computed(() => props.format === 'apk')
const apkBranch = ref(APK_DEFAULT_BRANCH)
const apkRepo = ref(APK_DEFAULT_REPO)
const branchError = computed(() => (isApk.value && !validApkSegment(apkBranch.value) ? t('upload.apk.invalid') : ''))
const repoError = computed(() => (isApk.value && !validApkSegment(apkRepo.value) ? t('upload.apk.invalid') : ''))
// Optional: only a noarch package needs the architecture directory; empty omits the field.
const apkArch = ref('')
const archError = computed(() => (isApk.value && apkArch.value !== '' && !validApkSegment(apkArch.value) ? t('upload.apk.invalid') : ''))
const coordinatesValid = computed(() => branchError.value === '' && repoError.value === '' && archError.value === '')

// The terraform publish API is a raw token-authenticated PUT per module version or
// provider platform (coordinates live in the URL), which the multipart dropzone
// cannot express, so the zone shows the curl commands instead.
const terraformPublish = computed(() => clientSetup('terraform', window.location.origin, props.repo)[0]?.publish ?? '')

const formatHint = computed(() => {
  switch (props.format) {
    case 'apk':
      return t('upload.hint.apk')
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
  if (coordinatesValid.value) for (const f of Array.from(target.files)) startFor(f)
  target.value = ''
}

function onDrop(ev: DragEvent): void {
  dragOver.value = false
  const files = ev.dataTransfer?.files
  if (!files || !coordinatesValid.value) return
  for (const f of Array.from(files)) startFor(f)
}

function apkFields(): Record<string, string> {
  return { branch: apkBranch.value, repo: apkRepo.value, ...(apkArch.value ? { arch: apkArch.value } : {}) }
}

async function startFor(file: File): Promise<void> {
  const entry = upload.add(file)
  const repoPath = `/api/v1/repositories/${encodeURIComponent(props.repo)}`
  const url = isApk.value
    ? `${repoPath}/apks`
    : `${repoPath}/packages${props.format ? `?format=${encodeURIComponent(props.format)}` : ''}`
  try {
    await upload.start(entry, { url, fields: isApk.value ? apkFields() : undefined })
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
