<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Every AI analysis of one repository, newest first, with a status filter. Shown to administrators only:
  the endpoint needs the manage permission.
-->
<template>
  <section :aria-label="t('rl.ai.caption')">
    <div role="group" :aria-label="t('rl.ai.filter')" class="mb-3 flex flex-wrap items-center gap-1.5" data-testid="ai-filter">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        :aria-pressed="option.value === status"
        :data-status="option.value"
        class="inline-flex h-8 items-center rounded-full border px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
        :class="option.value === status ? 'border-primary bg-primary-soft text-primary' : 'border-border-strong text-muted hover:text-foreground'"
        @click="setStatus(option.value)"
      >
        {{ option.label }}
      </button>
    </div>

    <p v-if="loading" class="text-sm text-muted" role="status">{{ t('common.loading') }}</p>
    <StateBlock v-else-if="error" variant="error" :title="t('security.ai.loadFailed')" :hint="error">
      <template #action><AppButton variant="secondary" size="sm" @click="load">{{ t('ui.state.retry') }}</AppButton></template>
    </StateBlock>
    <div v-else-if="analyses.length === 0" class="rounded-xl border border-border bg-surface" data-testid="ai-empty">
      <StateBlock variant="empty" :title="t('rl.ai.empty')" :hint="t('rl.ai.emptyHint')" />
    </div>
    <div v-else class="overflow-hidden rounded-xl border border-border bg-surface">
      <TableScroller>
        <table class="w-full text-left text-sm">
          <caption class="sr-only">{{ t('rl.ai.caption') }}</caption>
          <thead class="border-b border-border text-xs text-muted">
            <tr>
              <th scope="col" class="px-4 py-2 font-medium">{{ t('rl.ai.col.artifact') }}</th>
              <th scope="col" class="px-4 py-2 font-medium">{{ t('rl.ai.col.status') }}</th>
              <th scope="col" class="px-4 py-2 font-medium">{{ t('rl.ai.col.license') }}</th>
              <th scope="col" class="px-4 py-2 font-medium">{{ t('rl.ai.col.tags') }}</th>
              <th scope="col" class="px-4 py-2 font-medium">{{ t('rl.ai.col.updated') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="a in analyses" :key="a.id" data-testid="ai-row">
              <td class="px-4 py-2">
                <router-link v-if="a.artifact_name" :to="artifactPath(repo, a.artifact_name, a.artifact_version || undefined)" class="font-medium text-primary hover:underline">
                  {{ a.artifact_name }}<template v-if="a.artifact_version"> {{ a.artifact_version }}</template>
                </router-link>
                <span v-else class="font-mono text-xs text-muted">{{ a.artifact_id }}</span>
              </td>
              <td class="px-4 py-2">
                <StatusBadge :label="aiStatusLabel(a.status)" :variant="aiStatusVariant(a.status)" />
                <p v-if="a.error_message" class="mt-1 max-w-xs break-words font-mono text-xs text-danger">{{ a.error_message }}</p>
              </td>
              <td class="px-4 py-2">
                <template v-if="a.status === 'completed'">
                  <StatusBadge :label="aiLicenseLabel(a.license_status)" :variant="aiLicenseVariant(a.license_status)" />
                  <span v-if="a.license_name" class="ml-1.5 font-mono text-xs">{{ a.license_name }}</span>
                </template>
                <span v-else class="text-muted">–</span>
              </td>
              <td class="px-4 py-2">
                <div v-if="a.tags?.length" class="flex flex-wrap gap-1">
                  <span v-for="tag in a.tags" :key="tag" class="rounded-md border border-border px-1.5 py-0.5 text-caption">{{ tag }}</span>
                </div>
                <span v-else class="text-muted">–</span>
              </td>
              <td class="whitespace-nowrap px-4 py-2 text-muted">
                <time :datetime="a.updated_at" :title="formatDateTime(a.updated_at)">{{ formatDateTime(a.updated_at) }}</time>
              </td>
            </tr>
          </tbody>
        </table>
      </TableScroller>
      <Pagination :total="total" :page="page" :limit="LIMIT" @change="setPage" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import Pagination from '../Pagination.vue'
import StatusBadge from '../StatusBadge.vue'
import StateBlock from '../ui/StateBlock.vue'
import TableScroller from '../ui/TableScroller.vue'
import { listAIAnalyses, type AIAnalysis } from '../../api/ai'
import { isFeatureUnavailable } from '../../api/client'
import { artifactPath } from '../../lib/artifactPath'
import { aiLicenseLabel, aiLicenseVariant, aiStatusLabel, aiStatusVariant } from '../../lib/aiAnalysis'
import { formatDateTime } from '../../lib/datetime'
import { t } from '../../i18n'

const LIMIT = 20

const props = defineProps<{ repo: string }>()

const options = [
  { value: '', label: t('rl.ai.status.all') },
  { value: 'pending', label: t('security.ai.status.pending') },
  { value: 'processing', label: t('security.ai.status.processing') },
  { value: 'completed', label: t('security.ai.status.completed') },
  { value: 'failed', label: t('security.ai.status.failed') },
]

const status = ref('')
const page = ref(1)
const analyses = ref<AIAnalysis[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref('')

function setStatus(value: string): void {
  status.value = value
  page.value = 1
}

function setPage(p: number): void {
  page.value = p
}

let generation = 0
async function load(): Promise<void> {
  const mine = ++generation
  loading.value = true
  error.value = ''
  try {
    const res = await listAIAnalyses(props.repo, { status: status.value || undefined, limit: LIMIT, offset: (page.value - 1) * LIMIT })
    if (mine !== generation) return
    analyses.value = res.analyses ?? []
    total.value = res.total
  } catch (err) {
    if (mine !== generation) return
    analyses.value = []
    error.value = isFeatureUnavailable(err) ? t('security.ai.unavailable') : err instanceof Error ? err.message : t('security.ai.loadFailed')
  } finally {
    if (mine === generation) loading.value = false
  }
}

watch(() => [props.repo, status.value, page.value], () => void load(), { immediate: true })
</script>
