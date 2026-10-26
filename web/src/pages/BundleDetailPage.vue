<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="`${name} ${version}`" :description="bundle?.description || undefined">
      <template v-if="bundle" #meta>
        <StatusBadge :label="bundleStateLabel(bundle.state)" :variant="bundleStateVariant(bundle.state)" />
        <StatusBadge v-if="bundle.gpg_key_id" :label="t('bundles.signed')" variant="success" />
      </template>
      <template v-if="bundle && (status.primary !== null || menuItems.length > 0)" #actions>
        <div class="flex items-center gap-2">
          <AppButton
            v-if="status.primary === 'seal'"
            variant="primary"
            class="max-md:h-11"
            data-testid="primary-seal"
            :disabled="status.blocked"
            :aria-describedby="status.blocked ? 'bundle-notices' : undefined"
            @click="sealOpen = true"
          >
            {{ t('bundles.action.seal') }}
          </AppButton>
          <AppButton v-else-if="status.primary === 'promote'" variant="primary" class="max-md:h-11" data-testid="primary-promote" @click="promoteOpen = true">
            {{ tp('bundles.action.promoteTo', { stage: status.nextStage?.name ?? '' }) }}
          </AppButton>
          <AppButton v-else-if="status.primary === 'archive'" variant="secondary" class="max-md:h-11" data-testid="primary-archive" @click="archiveOpen = true">
            {{ t('bundles.action.archive') }}
          </AppButton>
          <BundleMoreMenu v-if="menuItems.length > 0" :items="menuItems" @select="onMenu" />
        </div>
      </template>
    </PageHeader>

    <div v-if="detail.state.value === 'loading'" class="text-muted" role="status">{{ t('common.loading') }}</div>

    <div v-else-if="!bundle" class="rounded-lg border border-border bg-surface">
      <StateBlock
        :variant="detail.state.value === 'error' ? 'error' : 'empty'"
        :title="detail.state.value === 'not-found' ? t('bundles.notFound.title') : undefined"
        :hint="detail.state.value === 'not-found' ? t('bundles.notFound.hint') : detail.error.value"
      >
        <template #action>
          <AppButton v-if="detail.state.value === 'error'" variant="secondary" @click="detail.reload()">{{ t('ui.state.retry') }}</AppButton>
          <AppButton v-else variant="secondary" to="/bundles">{{ t('bundles.back') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <div v-else class="space-y-5">
      <ul v-if="status.notices.length > 0" id="bundle-notices" class="space-y-2" data-testid="bundle-notices">
        <li
          v-for="n in status.notices"
          :key="n"
          class="rounded-lg border px-3 py-2 text-sm text-foreground"
          :class="NOTICE_WARN.has(n) ? 'border-warning/40 bg-warning-soft' : 'border-border bg-surface-hover/40'"
          role="status"
        >
          {{ t(NOTICE_TEXT[n]) }}
        </li>
      </ul>

      <BundleLifecycle :steps="steps" :label="t('bundles.lifecycle')" />

      <AppTabs :tabs="tabs" :label="t('bundles.tabs')">
        <template #panel-items>
          <div class="space-y-3">
            <div v-if="bundle.state === 'draft'" class="flex justify-end">
              <AppButton variant="secondary" class="max-md:h-11" data-testid="add-items" @click="itemsOpen = true">
                <PlusIcon class="h-4 w-4" aria-hidden="true" />
                {{ t('bundles.items.add') }}
              </AppButton>
            </div>
            <ListTable
              :columns="itemColumns"
              :caption="t('bundles.tab.items')"
              :query="items.list"
              :empty-title="t('bundles.items.empty')"
              :empty-hint="bundle.state === 'draft' ? t('bundles.items.emptyHint') : undefined"
            >
              <template #cell-path="{ row }"><span class="break-all font-mono text-xs">{{ row.artifact_path }}</span></template>
              <template #cell-repo="{ row }">
                <router-link :to="`/repos/${encodeURIComponent(detail.repoName(row.source_repo_id))}`" class="text-sm text-primary hover:underline">
                  {{ detail.repoName(row.source_repo_id) }}
                </router-link>
              </template>
              <template #cell-format="{ row }"><FormatBadge v-if="row.format" :format="row.format" size="sm" /></template>
              <template #cell-sha="{ row }"><ShortSha :sha="row.sha256" /></template>
              <template #cell-size="{ row }"><span class="tabular-nums">{{ formatBytes(row.size_bytes) }}</span></template>
              <template #cell-actions="{ row }">
                <AppButton
                  variant="ghost"
                  size="xs"
                  class="max-md:h-11"
                  :aria-label="`${t('bundles.items.remove')}: ${row.artifact_path}`"
                  @click="removeItem(row)"
                >
                  {{ t('bundles.items.remove') }}
                </AppButton>
              </template>
            </ListTable>
          </div>
        </template>

        <template #panel-promotions>
          <ListTable
            :columns="promotionColumns"
            :caption="t('bundles.tab.promotions')"
            :rows="detail.promotions.value"
            :total="detail.promotions.value.length"
            :limit="Math.max(detail.promotions.value.length, 1)"
            :empty-title="t('bundles.promotions.empty')"
            :empty-hint="t('bundles.promotions.emptyHint')"
          >
            <template #cell-stages="{ row }">
              <span class="font-mono text-xs">{{ row.from_stage || t('bundles.noStage') }} → {{ row.to_stage }}</span>
            </template>
            <template #cell-status="{ row }">
              <StatusBadge :label="promotionLabel(row.status)" :variant="promotionVariant(row.status)" />
              <p v-if="row.error_message" class="mt-1 max-w-xs text-xs text-danger">{{ row.error_message }}</p>
            </template>
            <template #cell-by="{ row }">{{ row.promoted_by }}</template>
            <template #cell-items="{ row }"><span class="tabular-nums">{{ row.item_count }}</span></template>
            <template #cell-started="{ row }"><RelativeTime :value="row.started_at" /></template>
            <template #cell-notes="{ row }"><span class="text-sm text-muted">{{ row.notes || '—' }}</span></template>
          </ListTable>
        </template>

        <template #panel-security>
          <ListTable
            :columns="scanColumns"
            :caption="t('bundles.tab.security')"
            :rows="scans.rows.value"
            :loading="scans.loading.value"
            row-key="repo"
            :total="scans.rows.value.length"
            :limit="Math.max(scans.rows.value.length, 1)"
          >
            <template #cell-repo="{ row }">
              <router-link :to="{ path: '/security', query: { type: 'vuln', repo: row.repo } }" class="text-primary hover:underline">{{ row.repo }}</router-link>
            </template>
            <template #cell-total="{ row }">
              <span v-if="row.failed" class="text-xs text-warning">{{ t('bundles.scans.failed') }}</span>
              <span v-else class="tabular-nums">{{ row.total }}</span>
            </template>
            <template #cell-critical="{ row }">
              <span v-if="!row.failed" class="tabular-nums" :class="row.critical > 0 ? 'font-medium text-danger' : ''">{{ row.critical }}</span>
            </template>
            <template #cell-high="{ row }"><span v-if="!row.failed" class="tabular-nums">{{ row.high }}</span></template>
          </ListTable>
        </template>

        <template #panel-verification>
          <section class="rounded-lg border border-border bg-surface p-4 sm:p-5" :aria-label="t('bundles.tab.verification')">
            <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm" data-testid="verification">
              <dt class="text-muted">{{ t('bundles.verify.signature') }}</dt>
              <dd class="flex flex-wrap items-center gap-2 text-foreground">
                <template v-if="bundle.gpg_key_id">
                  <span>{{ t('bundles.signed') }}</span>
                  <span class="break-all font-mono text-xs text-muted">{{ bundle.gpg_key_id }}</span>
                  <CopyButton :text="bundle.gpg_key_id" :label="t('bundles.verify.copyKey')" />
                </template>
                <span v-else>{{ bundle.state === 'draft' ? t('bundles.verify.signedAtSeal') : t('bundles.notSigned') }}</span>
              </dd>
              <template v-if="bundle.sealed_at">
                <dt class="text-muted">{{ t('bundles.verify.sealed') }}</dt>
                <dd class="text-foreground"><RelativeTime :value="bundle.sealed_at" /> · {{ bundle.sealed_by }}</dd>
              </template>
              <template v-if="bundle.archived_at">
                <dt class="text-muted">{{ t('bundles.verify.archived') }}</dt>
                <dd class="text-foreground"><RelativeTime :value="bundle.archived_at" /> · {{ bundle.archived_by }}</dd>
              </template>
              <dt class="text-muted">{{ t('bundles.verify.manifest') }}</dt>
              <dd class="flex flex-wrap gap-2">
                <template v-if="bundle.state !== 'draft'">
                  <AppButton variant="secondary" size="sm" class="max-md:h-11" data-testid="download-manifest" @click="downloadManifest">
                    <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
                    {{ t('bundles.action.manifest') }}
                  </AppButton>
                  <AppButton v-if="bundle.gpg_signature" variant="secondary" size="sm" class="max-md:h-11" data-testid="download-signature" @click="downloadSignature">
                    <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
                    {{ t('bundles.action.signature') }}
                  </AppButton>
                </template>
                <span v-else class="text-muted">{{ t('bundles.verify.manifestAtSeal') }}</span>
              </dd>
            </dl>
          </section>
        </template>
      </AppTabs>
    </div>

    <BundleItemsDrawer
      v-if="bundle"
      :open="itemsOpen"
      :name="name"
      :version="version"
      :repos="detail.repos.value"
      :existing="items.all.value"
      @close="itemsOpen = false"
      @added="onItemsAdded"
    />
    <BundleSealDrawer
      v-if="bundle"
      :open="sealOpen"
      :name="name"
      :version="version"
      :item-count="itemCount ?? 0"
      @close="sealOpen = false"
      @sealed="onSealed"
    />
    <BundlePromoteDrawer
      v-if="bundle"
      :open="promoteOpen"
      :bundle="bundle"
      :stages="promotableStages(detail.pipeline.value, bundle)"
      :repos="detail.repos.value"
      :item-count="itemCount ?? 0"
      :scans="scans.rows.value"
      @close="promoteOpen = false"
      @promoted="onPromoted"
    />

    <ConfirmDialog
      :open="archiveOpen"
      :title="t('bundles.archive.title')"
      :description="t('bundles.archive.description')"
      :confirm-label="t('bundles.action.archive')"
      tone="danger"
      :busy="busy"
      @confirm="archive"
      @close="archiveOpen = false"
    />
    <ConfirmDialog
      :open="deleteOpen"
      :title="t('bundles.delete.title')"
      :description="t('bundles.delete.description')"
      :confirm-label="t('bundles.action.delete')"
      tone="danger"
      :busy="busy"
      @confirm="remove"
      @close="deleteOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowDownTrayIcon, PlusIcon } from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import BundleItemsDrawer from '../components/bundles/BundleItemsDrawer.vue'
import BundleLifecycle from '../components/bundles/BundleLifecycle.vue'
import BundleMoreMenu, { type MoreMenuItem } from '../components/bundles/BundleMoreMenu.vue'
import BundlePromoteDrawer from '../components/bundles/BundlePromoteDrawer.vue'
import BundleSealDrawer from '../components/bundles/BundleSealDrawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import FormatBadge from '../components/FormatBadge.vue'
import PageHeader from '../components/PageHeader.vue'
import ShortSha from '../components/ShortSha.vue'
import StatusBadge from '../components/StatusBadge.vue'
import AppTabs from '../components/ui/AppTabs.vue'
import CopyButton from '../components/ui/CopyButton.vue'
import ListTable, { type ListColumn } from '../components/ui/ListTable.vue'
import RelativeTime from '../components/ui/RelativeTime.vue'
import StateBlock from '../components/ui/StateBlock.vue'
import {
  addBundleItems,
  archiveBundle,
  deleteBundle,
  listBundleItems,
  manifestUrl,
  removeBundleItem,
  signatureUrl,
  type BundleItem,
} from '../api/bundles'
import { downloadAttachment } from '../api/client'
import { useBundleDetail } from '../composables/useBundleDetail'
import { useBundleScans } from '../composables/useBundleScans'
import { useLocalList } from '../composables/useLocalList'
import type { TabDef } from '../composables/useUrlTab'
import {
  bundleStateLabel,
  bundleStateVariant,
  bundleStatus,
  lifecycleSteps,
  promotableStages,
  promotionLabel,
  promotionVariant,
  type BundleNotice,
  type BundleStatus,
} from '../lib/bundles'
import { formatBytes } from '../lib/quota'
import { useToastStore } from '../stores/toast'
import { t, tp, type TranslationKey } from '../i18n'

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()

const name = computed(() => String(route.params.name))
const version = computed(() => String(route.params.version))

const detail = useBundleDetail(() => name.value, () => version.value)
const bundle = detail.bundle

// The item count is unknown (null) until the items loaded; it only gates the seal action.
const itemCount = ref<number | null>(null)
const items = useLocalList<Record<string, never>, BundleItem>({
  filters: {},
  limit: 25,
  load: async () => {
    const rows = await listBundleItems(name.value, version.value)
    itemCount.value = rows.length
    return rows
  },
})

watch([name, version], () => {
  itemCount.value = null
  void items.refresh()
})

// Scans are looked up by repository name; items whose repository the caller cannot list are left out.
const scans = useBundleScans(() => {
  if (!bundle.value) return []
  const known = new Set(detail.repos.value.map((r) => r.id))
  return items.all.value.filter((i) => known.has(i.source_repo_id)).map((i) => detail.repoName(i.source_repo_id))
})

const status = computed<BundleStatus>(() =>
  bundle.value ? bundleStatus(bundle.value, detail.pipeline.value, itemCount.value) : { primary: null, blocked: false, notices: [], nextStage: null },
)
const steps = computed(() => (bundle.value ? lifecycleSteps(bundle.value, detail.pipeline.value, detail.promotions.value) : []))

const NOTICE_TEXT: Record<BundleNotice, TranslationKey> = {
  noItems: 'bundles.notice.noItems',
  unsigned: 'bundles.notice.unsigned',
  noPipeline: 'bundles.notice.noPipeline',
  pipelineUnavailable: 'bundles.notice.pipelineUnavailable',
  finalStage: 'bundles.notice.finalStage',
  archived: 'bundles.notice.archived',
}
const NOTICE_WARN = new Set<BundleNotice>(['noItems', 'unsigned', 'pipelineUnavailable'])

const tabs = computed<TabDef[]>(() => {
  const critical = scans.critical.value
  return [
    { id: 'items', label: t('bundles.tab.items'), count: itemCount.value ?? undefined },
    { id: 'promotions', label: t('bundles.tab.promotions'), count: detail.promotions.value.length },
    {
      id: 'security',
      label: t('bundles.tab.security'),
      hidden: scans.rows.value.length === 0 && !scans.loading.value,
      flag: critical > 0 ? tp('bundles.scans.criticalFlag', { count: String(critical) }) : undefined,
    },
    { id: 'verification', label: t('bundles.tab.verification') },
  ]
})

const itemColumns = computed<ListColumn[]>(() => [
  { key: 'path', label: t('bundles.items.column.path') },
  { key: 'repo', label: t('bundles.items.column.repo') },
  { key: 'format', label: t('bundles.items.column.format') },
  { key: 'sha', label: t('bundles.items.column.sha') },
  { key: 'size', label: t('bundles.items.column.size'), align: 'end' },
  ...(bundle.value?.state === 'draft' ? [{ key: 'actions', label: t('common.actions'), actions: true }] : []),
])
const promotionColumns = computed<ListColumn[]>(() => [
  { key: 'stages', label: t('bundles.promotions.column.stages') },
  { key: 'status', label: t('bundles.promotions.column.status') },
  { key: 'by', label: t('bundles.promotions.column.by') },
  { key: 'items', label: t('bundles.promotions.column.items'), align: 'end' },
  { key: 'started', label: t('bundles.promotions.column.started') },
  { key: 'notes', label: t('bundles.promotions.column.notes') },
])
const scanColumns = computed<ListColumn[]>(() => [
  { key: 'repo', label: t('bundles.scans.column.repo') },
  { key: 'total', label: t('bundles.scans.column.total'), align: 'end' },
  { key: 'critical', label: t('bundles.scans.critical'), align: 'end' },
  { key: 'high', label: t('bundles.scans.high'), align: 'end' },
])

// Secondary actions: the primary one is on the page, the rest hides behind the menu.
const menuItems = computed<MoreMenuItem[]>(() => {
  const b = bundle.value
  if (!b) return []
  const out: MoreMenuItem[] = []
  if (b.state !== 'draft') out.push({ id: 'manifest', label: t('bundles.action.manifest') })
  if (b.state !== 'draft' && b.gpg_signature) out.push({ id: 'signature', label: t('bundles.action.signature') })
  if (b.state === 'sealed' && status.value.primary === 'promote') out.push({ id: 'archive', label: t('bundles.action.archive'), danger: true })
  if (b.state === 'draft') out.push({ id: 'delete', label: t('bundles.action.deleteDraft'), danger: true })
  return out
})

const itemsOpen = ref(false)
const sealOpen = ref(false)
const promoteOpen = ref(false)
const archiveOpen = ref(false)
const deleteOpen = ref(false)
const busy = ref(false)

function onMenu(id: string): void {
  if (id === 'manifest') void downloadManifest()
  else if (id === 'signature') void downloadSignature()
  else if (id === 'archive') archiveOpen.value = true
  else if (id === 'delete') deleteOpen.value = true
}

async function download(url: string, fallback: string): Promise<void> {
  try {
    await downloadAttachment(url, fallback)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.action.downloadFailed'))
  }
}
const downloadManifest = (): Promise<void> => download(manifestUrl(name.value, version.value), `${name.value}-${version.value}.manifest.json`)
const downloadSignature = (): Promise<void> => download(signatureUrl(name.value, version.value), `${name.value}-${version.value}.manifest.asc`)

// Removing an item from a draft is reversible: the toast offers to put it back.
async function removeItem(item: BundleItem): Promise<void> {
  try {
    await removeBundleItem(name.value, version.value, item.sha256)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.items.removeFailed'))
    return
  }
  toasts.push({
    level: 'success',
    title: t('bundles.items.removed'),
    description: item.artifact_path,
    action: { label: t('bundles.items.undo'), run: () => void restoreItem(item) },
  })
  await items.refresh()
}

async function restoreItem(item: BundleItem): Promise<void> {
  try {
    await addBundleItems(name.value, version.value, [
      { source_repo_id: item.source_repo_id, sha256: item.sha256, artifact_path: item.artifact_path, format: item.format, size_bytes: item.size_bytes },
    ])
    await items.refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.items.addFailed'))
  }
}

async function onItemsAdded(): Promise<void> {
  itemsOpen.value = false
  await items.refresh()
}

async function onSealed(): Promise<void> {
  sealOpen.value = false
  await detail.reload()
}

async function onPromoted(): Promise<void> {
  promoteOpen.value = false
  await detail.reload()
}

async function archive(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await archiveBundle(name.value, version.value)
    toasts.success(t('bundles.archive.done'))
    archiveOpen.value = false
    await detail.reload()
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
