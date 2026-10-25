<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <PageHeader :title="`${name} ${version}`" :description="bundle?.description || undefined">
      <template v-if="bundle" #actions>
        <div class="flex items-center gap-2 flex-wrap">
          <StatusBadge :label="bundleStateLabel(bundle.state)" :variant="bundleStateVariant(bundle.state)" />
          <template v-if="bundle.state === 'draft'">
            <AppButton variant="secondary" size="sm" @click="itemOpen = true">
              <PlusIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('bundles.action.addItems') }}
            </AppButton>
            <AppButton variant="primary" size="sm" :disabled="items.length === 0" @click="sealOpen = true">
              <LockClosedIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('bundles.action.seal') }}
            </AppButton>
            <AppButton variant="danger-outline" size="sm" @click="deleteOpen = true">{{ t('bundles.action.delete') }}</AppButton>
          </template>
          <template v-else>
            <AppButton variant="secondary" size="sm" @click="download(manifestUrl(name, version), `${name}-${version}.manifest.json`)">
              <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('bundles.action.manifest') }}
            </AppButton>
            <AppButton
              v-if="bundle.gpg_signature"
              variant="secondary"
              size="sm"
              @click="download(signatureUrl(name, version), `${name}-${version}.manifest.asc`)"
            >
              <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('bundles.action.signature') }}
            </AppButton>
            <AppButton
              v-if="bundle.state === 'sealed'"
              variant="primary"
              size="sm"
              :disabled="promotable.length === 0"
              :title="promotable.length === 0 ? t('bundles.promote.noStages') : undefined"
              @click="promoteOpen = true"
            >
              <ArrowUpCircleIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('bundles.action.promote') }}
            </AppButton>
            <AppButton v-if="bundle.state === 'sealed'" variant="secondary" size="sm" @click="archiveOpen = true">
              {{ t('bundles.action.archive') }}
            </AppButton>
          </template>
        </div>
      </template>
    </PageHeader>

    <p v-if="loading" class="text-muted">{{ t('common.loading') }}</p>
    <p v-else-if="loadError" class="text-danger" role="alert">{{ loadError }}</p>

    <template v-else-if="bundle">
      <!-- Summary -->
      <section class="bg-surface rounded-xl border border-border p-5" :aria-label="t('bundles.detail.summary')">
        <dl class="grid grid-cols-1 sm:grid-cols-3 gap-x-6 gap-y-4 text-sm">
          <div>
            <dt class="text-muted">{{ t('bundles.detail.createdBy') }}</dt>
            <dd class="text-foreground">{{ bundle.created_by }} · {{ formatDateTime(bundle.created_at) }}</dd>
          </div>
          <div v-if="bundle.sealed_at">
            <dt class="text-muted">{{ t('bundles.detail.sealedBy') }}</dt>
            <dd class="text-foreground">{{ bundle.sealed_by }} · {{ formatDateTime(bundle.sealed_at) }}</dd>
          </div>
          <div v-if="bundle.archived_at">
            <dt class="text-muted">{{ t('bundles.detail.archivedBy') }}</dt>
            <dd class="text-foreground">{{ bundle.archived_by }} · {{ formatDateTime(bundle.archived_at) }}</dd>
          </div>
          <div v-if="bundle.state !== 'draft'">
            <dt class="text-muted">{{ t('bundles.detail.signedBy') }}</dt>
            <dd class="text-foreground">
              <span v-if="bundle.gpg_key_id" class="font-mono text-xs break-all" data-testid="signer">{{ bundle.gpg_key_id }}</span>
              <span v-else class="text-muted">{{ t('bundles.detail.unsigned') }}</span>
            </dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('bundles.detail.currentStage') }}</dt>
            <dd class="text-foreground">{{ bundle.current_stage ?? t('bundles.noStage') }}</dd>
          </div>
        </dl>
      </section>

      <!-- Pipeline -->
      <section class="bg-surface rounded-xl border border-border p-5" :aria-label="t('bundles.pipeline.trail')">
        <h2 class="text-sm font-semibold text-foreground mb-3">
          {{ t('bundles.pipeline.label') }}<span v-if="pipeline">: {{ pipeline.name }}</span>
        </h2>
        <p v-if="!pipeline" class="text-sm text-muted">{{ t('bundles.pipeline.none') }}</p>
        <ol v-else class="flex flex-wrap items-center gap-2" data-testid="stage-trail">
          <li v-for="(step, i) in steps" :key="step.name" class="flex items-center gap-2">
            <ChevronRightIcon v-if="i > 0" class="w-4 h-4 text-muted" aria-hidden="true" />
            <span
              class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-sm"
              :class="STEP_CLASS[step.status]"
              :aria-current="step.status === 'current' ? 'step' : undefined"
            >
              <CheckCircleIcon v-if="step.status === 'done'" class="w-4 h-4" aria-hidden="true" />
              {{ step.name }}
              <span class="sr-only">({{ t(STEP_LABEL[step.status]) }})</span>
            </span>
          </li>
        </ol>
      </section>

      <!-- Items -->
      <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('bundles.items.title')">
        <div class="px-4 py-3 border-b border-border flex items-center justify-between">
          <h2 class="text-sm font-semibold text-foreground">{{ t('bundles.items.title') }}</h2>
          <span class="text-xs text-muted">{{ tp('bundles.items.count', { n: String(items.length) }) }}</span>
        </div>
        <EmptyState v-if="items.length === 0" :title="t('bundles.items.empty')" :description="t('bundles.items.emptyHint')" />
        <DataTable v-else :columns="itemColumns" :rows="items as unknown as Record<string, unknown>[]">
          <template #cell-artifact_path="{ row }"><span class="font-mono text-xs break-all">{{ row.artifact_path }}</span></template>
          <template #cell-source_repo_id="{ row }">{{ repoName(row.source_repo_id as string) }}</template>
          <template #cell-format="{ row }"><FormatBadge :format="row.format as string" size="sm" /></template>
          <template #cell-sha256="{ row }"><ShortSha :sha="row.sha256 as string" /></template>
          <template #cell-size_bytes="{ row }">{{ formatBytes(row.size_bytes as number) }}</template>
          <template #cell-actions="{ row }">
            <AppButton
              v-if="bundle.state === 'draft'"
              variant="danger-outline"
              size="xs"
              :aria-label="`${t('bundles.items.remove')}: ${row.artifact_path}`"
              @click="removeItem(row.sha256 as string)"
            >
              {{ t('bundles.items.remove') }}
            </AppButton>
          </template>
        </DataTable>
      </section>

      <!-- Scans -->
      <section v-if="scanRows.length > 0" class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('bundles.scans.title')">
        <div class="px-4 py-3 border-b border-border">
          <h2 class="text-sm font-semibold text-foreground">{{ t('bundles.scans.title') }}</h2>
          <p class="text-xs text-muted mt-0.5">{{ t('bundles.scans.description') }}</p>
        </div>
        <DataTable :columns="scanColumns" :rows="scanRows as unknown as Record<string, unknown>[]" row-key="repo">
          <template #cell-repo="{ row }">
            <router-link :to="{ path: '/security/vulns', query: { repo: row.repo as string } }" class="text-primary hover:underline">
              {{ row.repo }}
            </router-link>
          </template>
          <template #cell-total="{ row }">
            <span v-if="row.error" class="text-danger">{{ t('bundles.scans.failed') }}</span>
            <span v-else-if="row.total === 0" class="text-muted">{{ t('bundles.scans.none') }}</span>
            <span v-else class="tabular-nums">{{ row.total }}</span>
          </template>
          <template #cell-critical="{ row }"><span class="tabular-nums">{{ row.error ? '—' : row.critical }}</span></template>
          <template #cell-high="{ row }"><span class="tabular-nums">{{ row.error ? '—' : row.high }}</span></template>
        </DataTable>
      </section>

      <!-- Promotion history -->
      <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('bundles.promotions.title')">
        <div class="px-4 py-3 border-b border-border">
          <h2 class="text-sm font-semibold text-foreground">{{ t('bundles.promotions.title') }}</h2>
        </div>
        <EmptyState v-if="promotions.length === 0" :title="t('bundles.promotions.empty')" />
        <DataTable v-else :columns="promotionColumns" :rows="promotions as unknown as Record<string, unknown>[]">
          <template #cell-to_stage="{ row }">
            {{ row.from_stage ?? t('bundles.promotions.firstStage') }} → <span class="font-medium">{{ row.to_stage }}</span>
          </template>
          <template #cell-status="{ row }">
            <StatusBadge :label="promotionLabel(row.status as PromotionStatus)" :variant="promotionVariant(row.status as PromotionStatus)" />
            <p v-if="row.error_message" class="text-xs text-danger mt-1">{{ row.error_message }}</p>
          </template>
          <template #cell-started_at="{ row }">{{ formatDateTime(row.started_at as string) }}</template>
        </DataTable>
      </section>
    </template>

    <BundleItemDialog :open="itemOpen" :repos="repos" :name="name" :version="version" @close="itemOpen = false" @added="onItemsAdded" />
    <BundleSealDialog :open="sealOpen" :name="name" :version="version" @close="sealOpen = false" @sealed="onSealed" />
    <BundlePromoteDialog
      v-if="bundle"
      :open="promoteOpen"
      :bundle="bundle"
      :stages="promotable"
      :repos="repos"
      @close="promoteOpen = false"
      @promoted="onPromoted"
    />
    <ConfirmDialog
      :open="archiveOpen"
      :title="t('bundles.archive.title')"
      :description="t('bundles.archive.description')"
      :confirm-label="t('bundles.action.archive')"
      tone="primary"
      :busy="busy"
      @confirm="archive"
      @close="archiveOpen = false"
    />
    <ConfirmDialog
      :open="deleteOpen"
      :title="t('bundles.delete.title')"
      :description="t('bundles.delete.description')"
      :confirm-label="t('bundles.action.delete')"
      :busy="busy"
      @confirm="remove"
      @close="deleteOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowDownTrayIcon,
  ArrowUpCircleIcon,
  CheckCircleIcon,
  ChevronRightIcon,
  LockClosedIcon,
  PlusIcon,
} from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import BundleItemDialog from '../components/BundleItemDialog.vue'
import BundlePromoteDialog from '../components/BundlePromoteDialog.vue'
import BundleSealDialog from '../components/BundleSealDialog.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import DataTable, { type Column } from '../components/DataTable.vue'
import EmptyState from '../components/EmptyState.vue'
import FormatBadge from '../components/FormatBadge.vue'
import PageHeader from '../components/PageHeader.vue'
import ShortSha from '../components/ShortSha.vue'
import StatusBadge from '../components/StatusBadge.vue'
import {
  archiveBundle,
  deleteBundle,
  getBundle,
  listBundleItems,
  listPipelines,
  listPromotions,
  manifestUrl,
  removeBundleItem,
  signatureUrl,
  type Bundle,
  type BundleItem,
  type BundlePromotion,
  type Pipeline,
  type PromotionStatus,
} from '../api/bundles'
import { downloadAttachment } from '../api/client'
import { listAllRepositories, type Repository } from '../api/repositories'
import { getVulnScanSummary } from '../api/security'
import {
  bundleStateLabel,
  bundleStateVariant,
  promotableStages,
  promotionLabel,
  promotionVariant,
  stageSteps,
  type StageStatus,
} from '../lib/bundles'
import { formatDateTime } from '../lib/datetime'
import { formatBytes } from '../lib/quota'
import { useToastStore } from '../stores/toast'
import { ApiError } from '../api/client'
import { t, tp, type TranslationKey } from '../i18n/en'

const STEP_CLASS: Record<StageStatus, string> = {
  done: 'border-success/30 bg-success-soft text-success',
  current: 'border-primary bg-primary-soft text-primary font-medium',
  pending: 'border-border bg-surface-hover text-muted',
}
const STEP_LABEL: Record<StageStatus, TranslationKey> = {
  done: 'bundles.pipeline.stage.done',
  current: 'bundles.pipeline.stage.current',
  pending: 'bundles.pipeline.stage.pending',
}

interface ScanRow {
  repo: string
  total: number
  critical: number
  high: number
  error: boolean
}

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()

const name = computed(() => String(route.params.name ?? ''))
const version = computed(() => String(route.params.version ?? ''))

const bundle = ref<Bundle | null>(null)
const items = ref<BundleItem[]>([])
const promotions = ref<BundlePromotion[]>([])
const pipelines = ref<Pipeline[]>([])
const repos = ref<Repository[]>([])
const scanRows = ref<ScanRow[]>([])
const loading = ref(false)
const loadError = ref('')
const busy = ref(false)

const itemOpen = ref(false)
const sealOpen = ref(false)
const promoteOpen = ref(false)
const archiveOpen = ref(false)
const deleteOpen = ref(false)

const pipeline = computed(() => pipelines.value.find((p) => p.id === bundle.value?.pipeline_id))
const steps = computed(() => stageSteps(pipeline.value, bundle.value?.current_stage ?? null))
const promotable = computed(() => (bundle.value ? promotableStages(pipeline.value, bundle.value) : []))

const itemColumns: Column[] = [
  { key: 'artifact_path', label: t('bundles.items.column.path') },
  { key: 'source_repo_id', label: t('bundles.items.column.repo') },
  { key: 'format', label: t('bundles.items.column.format') },
  { key: 'sha256', label: t('bundles.items.column.sha') },
  { key: 'size_bytes', label: t('bundles.items.column.size') },
  { key: 'actions', label: t('common.actions') },
]
const scanColumns: Column[] = [
  { key: 'repo', label: t('bundles.scans.repo') },
  { key: 'total', label: t('bundles.scans.total') },
  { key: 'critical', label: t('bundles.scans.critical') },
  { key: 'high', label: t('bundles.scans.high') },
]
const promotionColumns: Column[] = [
  { key: 'to_stage', label: t('bundles.promotions.column.stages') },
  { key: 'status', label: t('bundles.promotions.column.status') },
  { key: 'promoted_by', label: t('bundles.promotions.column.by') },
  { key: 'item_count', label: t('bundles.promotions.column.items') },
  { key: 'started_at', label: t('bundles.promotions.column.started') },
  { key: 'notes', label: t('bundles.promotions.column.notes') },
]

function repoName(id: string): string {
  return repos.value.find((r) => r.id === id)?.name ?? id
}

async function loadScans(): Promise<void> {
  const names = [...new Set(items.value.map((i) => repoName(i.source_repo_id)))].sort()
  scanRows.value = await Promise.all(
    names.map(async (repo): Promise<ScanRow> => {
      try {
        const s = await getVulnScanSummary(repo)
        return {
          repo,
          total: s.total,
          critical: s.severity_at_least.critical ?? 0,
          high: s.severity_at_least.high ?? 0,
          error: false,
        }
      } catch {
        return { repo, total: 0, critical: 0, high: 0, error: true }
      }
    }),
  )
}

let seq = 0

async function load(): Promise<void> {
  const mine = ++seq
  loading.value = bundle.value === null
  loadError.value = ''
  try {
    const [b, its, proms, pls, rs] = await Promise.all([
      getBundle(name.value, version.value),
      listBundleItems(name.value, version.value),
      listPromotions(name.value, version.value),
      listPipelines().catch(() => [] as Pipeline[]),
      listAllRepositories().catch(() => [] as Repository[]),
    ])
    if (mine !== seq) return
    bundle.value = b
    items.value = its
    promotions.value = proms
    pipelines.value = pls
    repos.value = rs
    await loadScans()
  } catch (err) {
    if (mine !== seq) return
    bundle.value = null
    loadError.value =
      err instanceof ApiError && err.status === 404
        ? t('bundles.notFound')
        : err instanceof Error
          ? err.message
          : t('bundles.detailLoadFailed')
  } finally {
    if (mine === seq) loading.value = false
  }
}

watch([name, version], () => {
  bundle.value = null
  void load()
}, { immediate: true })

async function download(url: string, fallback: string): Promise<void> {
  try {
    await downloadAttachment(url, fallback)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.action.downloadFailed'))
  }
}

async function removeItem(sha256: string): Promise<void> {
  try {
    await removeBundleItem(name.value, version.value, sha256)
    toasts.success(t('bundles.items.removed'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.items.removeFailed'))
  }
}

async function onItemsAdded(): Promise<void> {
  itemOpen.value = false
  await load()
}

async function onSealed(): Promise<void> {
  sealOpen.value = false
  await load()
}

async function onPromoted(): Promise<void> {
  promoteOpen.value = false
  await load()
}

async function archive(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await archiveBundle(name.value, version.value)
    toasts.success(t('bundles.archive.done'))
    archiveOpen.value = false
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.action.failed'))
  } finally {
    busy.value = false
  }
}

async function remove(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await deleteBundle(name.value, version.value)
    toasts.success(t('bundles.delete.done'))
    deleteOpen.value = false
    await router.push('/bundles')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.action.failed'))
  } finally {
    busy.value = false
  }
}
</script>
