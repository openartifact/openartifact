<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div v-if="entry" class="space-y-8">
    <section aria-labelledby="artifact-about">
      <h2 id="artifact-about" class="mb-2 text-base font-semibold text-foreground">{{ t('artifact.overview.about') }}</h2>
      <p v-if="description" class="max-w-3xl whitespace-pre-line break-words text-sm text-foreground">{{ description }}</p>
      <p v-else-if="ctx.detailError.value" role="alert" class="text-sm text-danger">{{ ctx.detailError.value }}</p>
      <p v-else-if="ctx.detail.value" class="text-sm text-muted">{{ t('artifact.overview.noDescription') }}</p>
      <ul class="mt-4 flex flex-wrap gap-x-8 gap-y-3" :aria-label="t('artifact.overview.glance')">
        <li v-for="g in glance" :key="g.tab">
          <router-link :to="{ query: { tab: g.tab } }" class="flex min-h-11 flex-col justify-center hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary">
            <span class="font-mono text-lg font-semibold tabular-nums">{{ g.value }}</span>
            <span class="text-xs text-muted">{{ g.label }}</span>
          </router-link>
        </li>
      </ul>
    </section>

    <section v-if="ctx.format.value === 'docker'" aria-labelledby="artifact-tags">
      <h2 id="artifact-tags" class="mb-2 text-base font-semibold text-foreground">{{ t('artifact.overview.tags') }}</h2>
      <ul class="flex flex-wrap gap-2">
        <li v-for="v in ctx.versions.value" :key="v.version">
          <router-link
            :to="versionTo(v.version)"
            :aria-current="v.version === entry.version ? 'true' : undefined"
            class="inline-flex min-h-8 items-center rounded-full border px-3 font-mono text-xs hover:text-foreground max-md:min-h-11"
            :class="v.version === entry.version ? 'border-foreground font-semibold text-foreground' : 'border-border text-muted'"
          >{{ v.version }}</router-link>
        </li>
      </ul>
    </section>

    <section v-if="metadata.length > 0" aria-labelledby="artifact-metadata">
      <h2 id="artifact-metadata" class="mb-2 text-base font-semibold text-foreground">{{ t('artifact.overview.metadata') }}</h2>
      <ListTable :columns="KV_COLUMNS" :rows="metadata" row-key="key" :caption="t('artifact.overview.metadata')" :limit="metadata.length">
        <template #cell-key="{ row }"><span class="font-mono text-xs">{{ row.key }}</span></template>
        <template #cell-value="{ row }"><span class="break-all font-mono text-xs">{{ row.value }}</span></template>
      </ListTable>
    </section>

    <section aria-labelledby="artifact-props">
      <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h2 id="artifact-props" class="text-base font-semibold text-foreground">{{ t('artifact.props.title') }}</h2>
        <AppButton v-if="ctx.caps.value.write && file" variant="ghost" size="sm" data-testid="props-edit" @click="editing = true">
          <PencilIcon class="h-4 w-4" aria-hidden="true" />{{ t('artifact.props.edit') }}
        </AppButton>
      </div>
      <ListTable
        :columns="KV_COLUMNS"
        :rows="propertyList"
        row-key="key"
        :caption="t('artifact.props.title')"
        :limit="Math.max(propertyList.length, 1)"
        :loading="propsLoading"
        :error="propsError"
        :empty-title="t('artifact.props.empty')"
        :empty-hint="t('artifact.props.emptyHint')"
        @retry="loadProperties"
      >
        <template #cell-key="{ row }"><span class="font-mono text-xs">{{ row.key }}</span></template>
        <template #cell-value="{ row }"><span class="break-all font-mono text-xs">{{ row.value }}</span></template>
      </ListTable>
      <p v-if="!ctx.caps.value.write" class="mt-2 text-xs text-muted">{{ t('artifact.props.readOnly') }}</p>
    </section>

    <ArtifactPropertiesDrawer
      v-if="file"
      :open="editing"
      :repo="ctx.repoName.value"
      :artifact-id="file.id"
      :label="`${ctx.pkgName.value} ${entry.version}`"
      :properties="properties"
      @close="editing = false"
      @saved="onSaved"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { PencilIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import ArtifactPropertiesDrawer from './ArtifactPropertiesDrawer.vue'
import { getArtifactProperties, type ArtifactProperties } from '../../api/artifactProperties'
import { ApiError } from '../../api/client'
import { useArtifactContext } from '../../composables/useArtifact'
import { artifactPath } from '../../lib/artifactPath'
import { countTotal, primaryFile } from '../../lib/artifactVersions'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const ctx = useArtifactContext()
const toasts = useToastStore()

const KV_COLUMNS: ListColumn[] = [
  { key: 'key', label: t('artifact.props.name') },
  { key: 'value', label: t('artifact.props.value') },
]

const entry = computed(() => ctx.entry.value)
const file = computed(() => (entry.value ? primaryFile(entry.value) : undefined))
const description = computed(() => ctx.detail.value?.description || ctx.detail.value?.summary || '')
const metadata = computed(() => ctx.detail.value?.metadata ?? [])

const glance = computed(() => {
  const counts = ctx.summary.value
  // Counts of a version nobody scanned are zeros, not "no findings".
  const findings = counts?.scanned_at ? String(countTotal(counts.vuln) + counts.license.violations + counts.secret.count) : '–'
  return [
    { tab: 'versions', label: t('artifact.tab.versions'), value: String(ctx.versions.value.length) },
    { tab: 'files', label: t('artifact.tab.files'), value: String(ctx.detail.value?.files.length ?? entry.value?.files.length ?? 0) },
    { tab: 'dependencies', label: t('artifact.tab.dependencies'), value: ctx.detail.value ? String(ctx.detail.value.dependencies.length) : '–' },
    { tab: 'security', label: t('artifact.overview.findings'), value: findings },
  ]
})

function versionTo(version: string): string {
  const latest = ctx.versions.value[0]?.version === version
  return artifactPath(ctx.repoName.value, ctx.pkgName.value, latest ? undefined : version)
}

const properties = ref<ArtifactProperties>({})
const propsLoading = ref(false)
const propsError = ref('')
const editing = ref(false)
const propertyList = computed(() => Object.keys(properties.value).sort().map((key) => ({ key, value: properties.value[key] ?? '' })))

let generation = 0
async function loadProperties(): Promise<void> {
  const target = file.value
  const mine = ++generation
  properties.value = {}
  propsError.value = ''
  if (!target) return
  propsLoading.value = true
  try {
    const res = await getArtifactProperties(ctx.repoName.value, target.id)
    if (mine === generation) properties.value = res ?? {}
  } catch (err) {
    if (mine !== generation) return
    // A format or repository without properties answers 404/501: that is an empty list, not an error.
    if (!(err instanceof ApiError && (err.status === 404 || err.status === 501))) {
      propsError.value = err instanceof Error ? err.message : t('artifact.props.loadFailed')
    }
  } finally {
    if (mine === generation) propsLoading.value = false
  }
}

function onSaved(next: ArtifactProperties): void {
  properties.value = next
  editing.value = false
  toasts.success(t('artifact.props.saved'))
}

watch(() => file.value?.id, () => void loadProperties(), { immediate: true })
</script>
