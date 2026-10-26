<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="mt-2 text-xs" data-testid="version-list">
    <p v-if="state === 'loading'" class="text-muted" role="status">{{ t('search.versionsLoading') }}</p>
    <p v-else-if="state === 'error'" class="text-danger" role="alert">{{ t('search.versionsFailed') }}</p>
    <ul v-else class="flex flex-col gap-0.5" role="list">
      <li v-for="v in versions" :key="v.id" class="flex flex-wrap items-center gap-x-3 rounded px-2 py-1 hover:bg-surface-hover max-md:min-h-11">
        <router-link :to="artifactPath(repo, name, v.version)" class="font-mono hover:underline">{{ v.version }}</router-link>
        <time class="ml-auto text-muted" :datetime="v.created_at" :title="formatDateTime(v.created_at)">{{ timeAgo(v.created_at) }}</time>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { listPackageVersions } from '../../api/repositories'
import { t } from '../../i18n/en'
import { artifactPath } from '../../lib/artifactPath'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import { distinctVersions, type VersionRow } from '../../lib/packageSearch'

const props = defineProps<{ repo: string; name: string; format: string }>()

const versions = ref<VersionRow[]>([])
const state = ref<'loading' | 'ready' | 'error'>('loading')

onMounted(async () => {
  try {
    versions.value = distinctVersions(await listPackageVersions(props.repo, props.name, props.format))
    state.value = 'ready'
  } catch {
    state.value = 'error'
  }
})
</script>
