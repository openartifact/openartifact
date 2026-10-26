<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The ordered members of a group repository: requests resolve against them in this order, the first match wins. -->
<template>
  <div>
    <p class="mb-3 text-sm text-muted">{{ t('repoDetail.group.hint') }}</p>
    <ListTable
      :columns="columns"
      :caption="t('repoDetail.group.title')"
      :rows="rows"
      :loading="loading"
      :error="error"
      :limit="1000"
      :row-key="(r) => r.name"
      :empty-title="t('repoDetail.group.emptyTitle')"
      :empty-hint="t('repoDetail.group.emptyHint')"
      @retry="load"
    >
      <template #cell-position="{ index }"><span class="font-mono text-xs text-muted">{{ index + 1 }}</span></template>
      <template #cell-name="{ row }">
        <router-link :to="`/repos/${encodeURIComponent(row.name)}`" class="font-mono text-sm font-semibold text-foreground hover:text-primary">{{ row.name }}</router-link>
      </template>
      <template #cell-type="{ row }">{{ row.repo_type === 'proxy' ? t('repos.new.type.proxy') : t('repos.new.type.hosted') }}</template>
    </ListTable>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import { listRepoMembers, type RepositoryMember } from '../../api/repositories'
import { t } from '../../i18n/en'

const props = defineProps<{ repo: string }>()

const rows = ref<RepositoryMember[]>([])
const loading = ref(true)
const error = ref('')

const columns = computed<ListColumn[]>(() => [
  { key: 'position', label: t('repoDetail.group.position') },
  { key: 'name', label: t('repos.column.name') },
  { key: 'type', label: t('repos.column.type') },
])

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    rows.value = await listRepoMembers(props.repo)
  } catch (err) {
    rows.value = []
    error.value = err instanceof Error ? err.message : t('repoDetail.group.loadFailed')
  } finally {
    loading.value = false
  }
}

watch(() => props.repo, load, { immediate: true })
</script>
