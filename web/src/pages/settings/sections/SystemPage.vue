<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > System > Health: the state of every component, and which build is running. -->
<template>
  <div class="space-y-5">
    <SectionActions>
      <AppButton variant="secondary" size="sm" class="max-md:h-11" :loading="loading" @click="load">{{ t('sx.refresh') }}</AppButton>
    </SectionActions>

    <div v-if="loading && !info" role="status" class="rounded-lg border border-border bg-surface px-4 py-5 text-sm text-muted">
      {{ t('common.loading') }}
    </div>

    <div v-else-if="!info" class="rounded-lg border border-border bg-surface" data-testid="health-error">
      <StateBlock variant="error" :title="t('sx.health.error.title')" :hint="error || t('sx.health.error.body')">
        <template #action>
          <AppButton variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <template v-else>
      <p class="flex flex-wrap items-center gap-2 text-sm" role="status" data-testid="health-summary">
        <StatusBadge :label="summary.text" :variant="BADGE[summary.status]" />
        <span class="text-muted">{{ tp('sx.health.checked', { time: `${formatUtc(checkedAt)} UTC` }) }}</span>
      </p>

      <ListTable
        :columns="columns"
        :caption="t('sx.health.table')"
        :rows="rows"
        row-key="id"
        :loading="loading"
      >
        <template #cell-component="{ row }">
          <div class="min-w-0">
            <span class="font-medium text-foreground">{{ (row as HealthRow).name }}</span>
            <p class="text-xs text-muted">{{ (row as HealthRow).sub }}</p>
          </div>
        </template>
        <template #cell-status="{ row }">
          <StatusBadge :label="t(`sx.health.status.${(row as HealthRow).status}`)" :variant="BADGE[(row as HealthRow).status]" />
        </template>
        <template #cell-detail="{ row }">
          <span class="text-muted">{{ (row as HealthRow).detail }}</span>
          <a
            v-if="(row as HealthRow).docs"
            :href="(row as HealthRow).docs"
            target="_blank"
            rel="noopener"
            class="ml-2 text-primary hover:underline"
          >{{ t('sx.scanners.guide') }}</a>
        </template>
        <template #cell-actions="{ row }">
          <AppButton
            v-if="(row as HealthRow).to"
            variant="ghost"
            size="sm"
            class="max-md:min-h-11"
            :to="(row as HealthRow).to"
            :aria-label="tp('sx.health.detailsOf', { name: (row as HealthRow).name })"
          >{{ (row as HealthRow).toLabel }}</AppButton>
        </template>
      </ListTable>

      <section class="rounded-lg border border-border bg-surface p-4" data-testid="build-info">
        <h2 class="text-sm font-semibold text-foreground">{{ t('sx.health.build') }}</h2>
        <p class="mt-0.5 text-sm text-muted">{{ t('sx.health.build.hint') }}</p>

        <div
          v-if="unstamped"
          class="mt-3 rounded-lg border border-warning bg-warning-soft p-3 text-sm text-foreground"
          role="status"
          data-testid="unstamped"
        >
          <p class="flex items-start gap-2 font-medium">
            <ExclamationTriangleIcon class="mt-0.5 h-4 w-4 shrink-0 text-warning" aria-hidden="true" />
            {{ t('sx.health.unstamped.title') }}
          </p>
          <p class="mt-1 text-muted">{{ t('sx.health.unstamped.body') }}</p>
          <p class="mt-2 text-xs text-muted">{{ t('sx.health.unstamped.example') }}</p>
          <div class="mt-1 flex items-start gap-2">
            <code class="min-w-0 flex-1 break-all rounded-md bg-surface px-3 py-2 font-mono text-xs text-foreground" data-testid="ldflags">{{ LDFLAGS_EXAMPLE }}</code>
            <CopyButton :text="LDFLAGS_EXAMPLE" :label="t('sx.health.unstamped.example')" />
          </div>
        </div>

        <dl class="mt-4 grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
          <div>
            <dt class="text-muted">{{ t('sx.health.build.version') }}</dt>
            <dd class="font-mono text-foreground" data-testid="build-version">{{ version ?? t('sx.health.build.notRecorded') }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('sx.health.build.commit') }}</dt>
            <dd class="flex items-center gap-1 font-mono text-foreground" data-testid="build-commit">
              <template v-if="commit">
                <span :title="info.commit">{{ commit }}</span>
                <CopyButton :text="info.commit ?? commit" :label="tp('sx.health.build.copyCommit', { sha: commit })" />
              </template>
              <span v-else class="font-sans text-muted">{{ t('sx.health.build.notRecorded') }}</span>
            </dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('sx.health.build.built') }}</dt>
            <dd class="font-mono text-foreground" :title="info.build_time">{{ built ?? t('sx.health.build.notRecorded') }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('sx.health.build.go') }}</dt>
            <dd class="font-mono text-foreground">{{ info.go_version || '—' }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('sx.health.build.platform') }}</dt>
            <dd class="font-mono text-foreground">{{ [info.os, info.arch].filter(Boolean).join(' / ') || '—' }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('sx.health.build.uptime') }}</dt>
            <dd class="font-mono text-foreground">{{ info.uptime || '—' }}</dd>
          </div>
        </dl>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../../components/AppButton.vue'
import SectionActions from '../../../components/SectionActions.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import CopyButton from '../../../components/ui/CopyButton.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import StateBlock from '../../../components/ui/StateBlock.vue'
import { getAdminSystemInfo, type AdminSystemInfo } from '../../../api/admin'
import { getSecurityHealth, type SecurityHealth } from '../../../api/security'
import { formatVersion } from '../../../composables/useAppVersion'
import { formatBuildTime, formatCommit, isStamped, isUnstampedBuild, LDFLAGS_EXAMPLE } from '../../../lib/buildInfo'
import { formatUtc } from '../../../lib/datetime'
import { healthRows, healthSummary, type HealthRow, type HealthStatus } from '../../../lib/systemHealth'
import { t, tp } from '../../../i18n'

const BADGE: Record<HealthStatus, 'success' | 'warning' | 'danger' | 'neutral'> = { ok: 'success', warn: 'warning', bad: 'danger', none: 'neutral' }

const info = ref<AdminSystemInfo | null>(null)
const security = ref<SecurityHealth | null>(null)
const securityError = ref('')
const error = ref('')
const loading = ref(true)
const checkedAt = ref(new Date())

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  const [sys, sec] = await Promise.allSettled([getAdminSystemInfo(), getSecurityHealth()])
  if (sys.status === 'fulfilled') {
    info.value = sys.value
  } else {
    info.value = null
    error.value = sys.reason instanceof Error ? sys.reason.message : ''
  }
  security.value = sec.status === 'fulfilled' ? sec.value : null
  securityError.value = sec.status === 'rejected' ? (sec.reason instanceof Error ? sec.reason.message : t('sx.scanners.loadFailed')) : ''
  checkedAt.value = new Date()
  loading.value = false
}
onMounted(load)

const rows = computed(() => (info.value ? healthRows(info.value, { health: security.value, error: securityError.value }) : []))
const summary = computed(() => healthSummary(rows.value))

const unstamped = computed(() => (info.value ? isUnstampedBuild(info.value) : false))
const version = computed(() => (info.value && isStamped(info.value.version) && info.value.version !== 'dev' ? formatVersion(info.value.version) : null))
const commit = computed(() => formatCommit(info.value?.commit))
const built = computed(() => formatBuildTime(info.value?.build_time))

const columns: ListColumn[] = [
  { key: 'component', label: t('sx.health.col.component') },
  { key: 'status', label: t('sx.health.col.status') },
  { key: 'detail', label: t('sx.health.col.detail') },
  { key: 'actions', label: t('sx.health.col.actions'), actions: true },
]
</script>
