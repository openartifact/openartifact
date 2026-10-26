<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section aria-labelledby="artifact-files">
    <h2 id="artifact-files" class="mb-3 text-base font-semibold text-foreground">{{ t('artifact.files.title') }}</h2>
    <p v-if="ctx.detailError.value" role="alert" class="text-sm text-danger">{{ ctx.detailError.value }}</p>
    <ListTable
      v-else
      :columns="columns"
      :query="list"
      row-key="id"
      :caption="tp('artifact.files.caption', { name: ctx.pkgName.value, version: ctx.entry.value?.version ?? '' })"
      :empty-title="t('artifact.files.empty')"
    >
      <template #cell-filename="{ row }"><span class="break-all font-mono text-xs">{{ row.filename }}</span></template>
      <template #cell-arch="{ row }"><span class="text-xs text-muted">{{ row.arch || '—' }}</span></template>
      <template #cell-size="{ row }"><span class="font-mono text-xs tabular-nums">{{ formatBytes(row.size) }}</span></template>
      <template #cell-sha256="{ row }">
        <span class="inline-flex items-center gap-1">
          <span class="font-mono text-xs" :title="row.sha256">{{ row.sha256.slice(0, 12) }}</span>
          <CopyButton :text="row.sha256" :label="tp('artifact.files.copySha', { file: row.filename })" />
        </span>
      </template>
      <template #cell-actions="{ row }">
        <AppButton variant="ghost" size="sm" class="max-md:h-11" :loading="downloading === row.id" :aria-label="tp('artifact.files.download', { file: row.filename })" @click="download(row)">
          <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
          <span class="md:sr-only">{{ t('artifact.action.download') }}</span>
        </AppButton>
      </template>
    </ListTable>

    <dl v-if="checksums.length > 0" class="mt-6 space-y-2 text-sm" data-testid="artifact-checksums">
      <div v-for="c in checksums" :key="c.label" class="flex flex-wrap items-center gap-x-3">
        <dt class="w-20 text-xs text-muted">{{ c.label }}</dt>
        <dd class="flex min-w-0 items-center gap-1">
          <span class="break-all font-mono text-xs">{{ c.value }}</span>
          <CopyButton :text="c.value" :label="tp('artifact.files.copyChecksum', { algorithm: c.label })" />
        </dd>
      </div>
    </dl>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import CopyButton from '../ui/CopyButton.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import type { PackageFile } from '../../api/artifact'
import { downloadPackage } from '../../api/repositories'
import { useArtifactContext } from '../../composables/useArtifact'
import { useLocalList } from '../../composables/useLocalList'
import { formatBytes } from '../../lib/quota'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const ctx = useArtifactContext()
const toasts = useToastStore()

const columns: ListColumn[] = [
  { key: 'filename', label: t('artifact.files.column.name') },
  { key: 'arch', label: t('artifact.files.column.arch') },
  { key: 'size', label: t('artifact.files.column.size'), align: 'end' },
  { key: 'sha256', label: t('artifact.files.column.sha256') },
  { key: 'actions', label: t('artifact.files.column.download'), actions: true },
]

const { list, refresh } = useLocalList({
  filters: {},
  load: async (): Promise<PackageFile[]> => ctx.detail.value?.files ?? [],
})

// SHA-1 and MD5 are stored by some formats only; the primary file's SHA-256 is in the table.
const checksums = computed(() => {
  const d = ctx.detail.value
  if (!d) return []
  return [
    ...(d.sha1 ? [{ label: 'SHA-1', value: d.sha1 }] : []),
    ...(d.md5 ? [{ label: 'MD5', value: d.md5 }] : []),
  ]
})

const downloading = ref('')
async function download(file: PackageFile): Promise<void> {
  downloading.value = file.id
  try {
    await downloadPackage(ctx.repoName.value, { id: file.id, name: file.filename, version: ctx.entry.value?.version ?? '' })
  } catch (err) {
    toasts.error(t('artifact.action.downloadFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    downloading.value = ''
  }
}

watch(ctx.detail, () => void refresh())
</script>
