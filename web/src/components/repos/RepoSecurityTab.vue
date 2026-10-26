<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Security counts of one repository from its latest scans, for everyone who can read it.
  Counts only: the findings themselves are on the Security page, which administrators open from here.
-->
<template>
  <div>
    <div v-if="loading" role="status" class="rounded-lg border border-border bg-surface p-6" data-testid="security-loading">
      <span class="sr-only">{{ t('ui.table.loading') }}</span>
      <div class="h-3 w-1/3 animate-pulse rounded bg-surface-hover" aria-hidden="true" />
    </div>
    <div v-else-if="error" class="rounded-lg border border-border bg-surface">
      <StateBlock variant="error" :hint="error">
        <template #action><AppButton variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton></template>
      </StateBlock>
    </div>
    <div v-else-if="summary && summary.scanned_at === null" class="rounded-lg border border-border bg-surface">
      <StateBlock variant="empty" :title="t('rl.security.noScanTitle')" :hint="t('rl.security.noScanHint')" />
    </div>
    <section v-else-if="summary" class="rounded-lg border border-border bg-surface" :aria-label="t('rl.tab.security')">
      <div class="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
        <p class="text-sm text-muted">
          {{ t('rl.security.scanned') }}
          <RelativeTime :value="summary.scanned_at" class="font-medium text-foreground" />
        </p>
        <p v-if="summary.quarantined" class="inline-flex items-center gap-1.5 text-sm font-medium text-danger" data-testid="quarantined">
          <ShieldExclamationIcon class="h-4 w-4" aria-hidden="true" />{{ t('rl.security.quarantined') }}
        </p>
        <AppButton v-if="session.isAdmin" variant="secondary" size="sm" :to="{ path: '/security', query: { repo: repo } }">{{ t('rl.security.openFindings') }}</AppButton>
      </div>
      <dl class="grid gap-px bg-border sm:grid-cols-2 lg:grid-cols-4">
        <div class="bg-surface px-4 py-3">
          <dt class="text-xs text-muted">{{ t('rl.security.vulns') }}</dt>
          <dd class="mt-1 flex flex-wrap gap-x-3 gap-y-0.5" data-testid="vuln-counts">
            <template v-if="vulnTotal > 0">
              <SeverityBadge v-for="s in SEVERITIES.filter((k) => summary!.vuln[k] > 0)" :key="s" :severity="s" :count="summary!.vuln[s]" />
            </template>
            <span v-else class="text-sm text-foreground">{{ t('rl.security.none') }}</span>
          </dd>
        </div>
        <div class="bg-surface px-4 py-3">
          <dt class="text-xs text-muted">{{ t('rl.security.licenseViolations') }}</dt>
          <dd class="mt-1 text-sm tabular-nums text-foreground" data-testid="license-count">{{ summary.license.violations }}</dd>
        </div>
        <div class="bg-surface px-4 py-3">
          <dt class="text-xs text-muted">{{ t('rl.security.secretFindings') }}</dt>
          <dd class="mt-1 text-sm tabular-nums text-foreground" data-testid="secret-count">{{ summary.secret.count }}</dd>
        </div>
        <div class="bg-surface px-4 py-3">
          <dt class="text-xs text-muted">{{ t('rl.security.aiReview') }}</dt>
          <dd class="mt-1 text-sm text-foreground" data-testid="ai-count">
            {{ tp('rl.security.aiCounts', { quarantine: String(summary.ai.quarantine_recommended), review: String(summary.ai.review_required) }) }}
          </dd>
        </div>
      </dl>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ShieldExclamationIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import SeverityBadge from '../ui/SeverityBadge.vue'
import StateBlock from '../ui/StateBlock.vue'
import { getRepoSecuritySummary, type RepoSecuritySummary } from '../../api/repositories'
import { useSessionStore } from '../../stores/session'
import { t, tp } from '../../i18n'

const SEVERITIES = ['critical', 'high', 'medium', 'low'] as const

const props = defineProps<{ repo: string }>()
const session = useSessionStore()

const summary = ref<RepoSecuritySummary | null>(null)
const loading = ref(true)
const error = ref('')
let generation = 0

const vulnTotal = computed(() => (summary.value ? SEVERITIES.reduce((n, s) => n + summary.value!.vuln[s], 0) : 0))

async function load(): Promise<void> {
  const mine = ++generation
  loading.value = true
  error.value = ''
  try {
    const res = await getRepoSecuritySummary(props.repo)
    if (mine === generation) summary.value = res
  } catch (err) {
    if (mine === generation) error.value = err instanceof Error ? err.message : t('rl.security.loadFailed')
  } finally {
    if (mine === generation) loading.value = false
  }
}

watch(() => props.repo, load, { immediate: true })
</script>
