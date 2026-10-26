<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Facts about the shown version. No card: rules and whitespace only. Only what the API reports is listed. -->
<template>
  <aside :aria-label="t('artifact.aside.title')" data-testid="artifact-aside">
    <h2 class="sr-only">{{ t('artifact.aside.title') }}</h2>
    <dl class="divide-y divide-border text-sm">
      <div class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.repository') }}</dt>
        <dd class="mt-0.5 flex flex-wrap items-center gap-2">
          <router-link :to="`/repos/${encodeURIComponent(ctx.repoName.value)}`" class="break-all font-mono text-primary hover:underline">{{ ctx.repoName.value }}</router-link>
          <span v-if="repoType" class="rounded-full border border-border px-2 py-0.5 text-micro text-muted">{{ t(`artifact.repoType.${repoType}`) }}</span>
        </dd>
      </div>
      <div class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.format') }}</dt>
        <dd class="mt-0.5">{{ formatLabel(ctx.format.value) }}</dd>
      </div>
      <div v-if="entry" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.version') }}</dt>
        <dd class="mt-0.5 break-all font-mono">{{ entry.version }}</dd>
      </div>
      <div v-if="entry?.createdAt" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.published') }}</dt>
        <dd class="mt-0.5">{{ formatDateTime(entry.createdAt) }}</dd>
      </div>
      <div v-if="entry && entry.size > 0" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.size') }}</dt>
        <dd class="mt-0.5 font-mono tabular-nums">{{ formatBytes(entry.size) }}</dd>
      </div>
      <div v-if="ctx.detail.value?.license" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.license') }}</dt>
        <dd class="mt-0.5 break-words">{{ ctx.detail.value.license }}</dd>
      </div>
      <div v-if="homepage" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.homepage') }}</dt>
        <dd class="mt-0.5 break-all"><a :href="homepage" target="_blank" rel="noopener noreferrer" class="text-primary hover:underline">{{ homepage }}</a></dd>
      </div>
      <div v-if="ctx.detail.value?.sha256" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.sha256') }}</dt>
        <dd class="mt-0.5 flex items-center gap-1">
          <span class="font-mono text-xs" :title="ctx.detail.value.sha256">{{ ctx.detail.value.sha256.slice(0, 16) }}…</span>
          <CopyButton :text="ctx.detail.value.sha256" :label="tp('artifact.aside.copySha', { file: ctx.detail.value.filename })" />
        </dd>
      </div>
      <div v-if="project" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.project') }}</dt>
        <dd class="mt-0.5"><router-link :to="`/projects/${encodeURIComponent(project)}`" class="text-primary hover:underline">{{ project }}</router-link></dd>
      </div>
      <div v-if="ctx.summary.value" class="py-2.5">
        <dt class="text-xs text-muted">{{ t('artifact.aside.lastScanned') }}</dt>
        <dd class="mt-0.5" data-testid="aside-scanned">
          {{ ctx.summary.value.scanned_at ? formatDateTime(ctx.summary.value.scanned_at) : t('artifact.aside.neverScanned') }}
        </dd>
      </div>
    </dl>
  </aside>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import CopyButton from '../ui/CopyButton.vue'
import { useArtifactContext } from '../../composables/useArtifact'
import { formatDateTime } from '../../lib/datetime'
import { formatLabel } from '../../lib/formats'
import { formatBytes } from '../../lib/quota'
import { safeHttpUrl } from '../../lib/artifactLinks'
import { t, tp } from '../../i18n'

const ctx = useArtifactContext()
const entry = computed(() => ctx.entry.value)
const repoType = computed(() => ctx.repository.value?.repo_type)
const project = computed(() => ctx.repository.value?.project)
// A homepage comes from package metadata: only http(s) links become anchors.
const homepage = computed(() => safeHttpUrl(ctx.detail.value?.homepage))
</script>
