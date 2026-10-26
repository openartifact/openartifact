<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Operations > Federation: the server state and every federated repository with how its peers are doing. -->
<template>
  <div class="space-y-5">
    <SectionActions>
      <AppButton variant="secondary" size="sm" class="max-md:h-11" :loading="loading" @click="refresh">{{ t('sx.refresh') }}</AppButton>
    </SectionActions>

    <div v-if="loading && !overview" role="status" class="rounded-lg border border-border bg-surface px-4 py-5 text-sm text-muted">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="error && !overview" class="rounded-lg border border-border bg-surface">
      <StateBlock variant="error" :hint="error">
        <template #action>
          <AppButton variant="secondary" @click="refresh">{{ t('ui.state.retry') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <template v-else-if="overview">
      <section
        v-if="overview.serverEnabled === false"
        class="rounded-lg border border-border bg-surface p-4"
        data-testid="federation-off"
      >
        <h2 class="flex items-center gap-2 text-sm font-semibold text-foreground">
          <StatusBadge :label="t('sx.fed.off.badge')" variant="neutral" />
          {{ t('sx.fed.off.title') }}
        </h2>
        <p class="mt-1 text-sm text-muted">{{ t('sx.fed.off.body') }}</p>
        <code class="mt-2 block rounded-md bg-surface-hover px-3 py-2 font-mono text-xs text-foreground">{{ t('sx.fed.off.command') }}</code>
      </section>

      <p v-else-if="overview.serverEnabled === null" class="rounded-lg border border-border bg-surface p-4 text-sm text-muted" data-testid="federation-unknown">
        {{ t('sx.fed.noRepos') }}
      </p>

      <template v-else>
        <section class="rounded-lg border border-border bg-surface p-4" data-testid="federation-on">
          <h2 class="flex items-center gap-2 text-sm font-semibold text-foreground">
            <StatusBadge :label="t('sx.fed.on.badge')" variant="success" />
            {{ t('sx.fed.on.title') }}
          </h2>
          <p class="mt-1 text-sm text-muted">{{ t('sx.fed.on.body') }}</p>
          <p class="mt-2 text-sm text-muted">
            {{ t('sx.fed.instance') }}:
            <span class="font-mono text-foreground">{{ overview.instanceId }}</span>
            <CopyButton :text="overview.instanceId" :label="t('sx.fed.instance')" />
          </p>
          <p class="mt-2 text-sm text-foreground" data-testid="federation-summary">{{ summary }}</p>
        </section>

        <p v-if="overview.failed.length > 0" class="text-sm text-warning" role="status" data-testid="federation-partial">
          {{ tp('sx.fed.partial', { names: overview.failed.join(', ') }) }}
        </p>

        <ListTable
          :columns="columns"
          :caption="t('sx.fed.table')"
          :rows="rows"
          row-key="repository"
          :empty-title="t('sx.fed.empty')"
          :empty-hint="t('sx.fed.emptyHint')"
        >
          <template #cell-repository="{ row }">
            <div class="flex flex-wrap items-center gap-2">
              <RouterLink
                :to="repoPath(row as FederationRepoRow)"
                class="font-mono text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              >{{ (row as FederationRepoRow).repository }}</RouterLink>
              <FormatBadge :format="(row as FederationRepoRow).format" size="sm" />
            </div>
          </template>
          <template #cell-peers="{ row }">
            <span v-if="(row as FederationRepoRow).peers.length > 0" class="tabular-nums">{{ tp('sx.fed.peerCount', { n: String((row as FederationRepoRow).peers.length) }) }}</span>
            <span v-else class="text-muted">{{ t('sx.fed.noPeers') }}</span>
          </template>
          <template #cell-state="{ row }">
            <StatusBadge :label="stateLabel(row as FederationRepoRow)" :variant="STATE_VARIANT[(row as FederationRepoRow).state]" />
          </template>
          <template #cell-log="{ row }">
            <span class="font-mono tabular-nums">{{ (row as FederationRepoRow).headSeq }}</span>
          </template>
          <template #cell-actions="{ row }">
            <AppButton
              variant="ghost"
              size="sm"
              class="max-md:min-h-11"
              :to="repoPath(row as FederationRepoRow)"
              :aria-label="tp('sx.fed.manage', { name: (row as FederationRepoRow).repository })"
            >{{ t('sx.open') }}</AppButton>
          </template>
        </ListTable>
      </template>

      <section class="rounded-lg border border-border bg-surface p-4" data-testid="federation-tokens">
        <h2 class="text-sm font-semibold text-foreground">{{ t('sx.fed.tokens.title') }}</h2>
        <p class="mt-0.5 text-sm text-muted">{{ t('sx.fed.tokens.body') }}</p>
        <AppButton variant="secondary" size="sm" class="mt-3 max-md:h-11" to="/settings/tokens?type=federation">{{ t('sx.fed.tokens.link') }}</AppButton>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppButton from '../../../components/AppButton.vue'
import CopyButton from '../../../components/ui/CopyButton.vue'
import FormatBadge from '../../../components/FormatBadge.vue'
import SectionActions from '../../../components/SectionActions.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import StateBlock from '../../../components/ui/StateBlock.vue'
import { loadFederationOverview, type FederationOverview, type FederationRepoRow, type SyncState } from '../../../lib/federationOverview'
import { t, tp } from '../../../i18n'

const STATE_VARIANT: Record<SyncState, 'success' | 'warning' | 'neutral'> = { ok: 'success', attention: 'warning', pending: 'neutral', none: 'neutral' }

const overview = ref<FederationOverview | null>(null)
const loading = ref(true)
const error = ref('')

async function refresh(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    overview.value = await loadFederationOverview()
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('sx.fed.loadFailed')
  } finally {
    loading.value = false
  }
}
onMounted(refresh)

const rows = computed(() => overview.value?.rows ?? [])
const summary = computed(() =>
  tp('sx.fed.summary', {
    repos: String(rows.value.length),
    peers: String(rows.value.reduce((n, r) => n + r.peers.length, 0)),
  }),
)

const columns: ListColumn[] = [
  { key: 'repository', label: t('sx.fed.col.repository') },
  { key: 'peers', label: t('sx.fed.col.peers') },
  { key: 'state', label: t('sx.fed.col.state') },
  { key: 'log', label: t('sx.fed.col.log') },
  { key: 'actions', label: t('sx.fed.col.actions'), actions: true },
]

const repoPath = (row: FederationRepoRow): string => `/repos/${encodeURIComponent(row.repository)}?tab=federation`

function stateLabel(row: FederationRepoRow): string {
  if (row.state === 'attention') return tp('sx.fed.state.attention', { n: String(row.attention) })
  return t(`sx.fed.state.${row.state}`)
}
</script>
