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
              <template #cell-path="{ row }"><span class="block truncate font-mono text-xs" :title="row.artifact_path">{{ row.artifact_path }}</span></template>
              <template #cell-repo="{ row }">
                <router-link :to="`/repos/${encodeURIComponent(detail.repoName(row.source_repo_id))}`" class="text-sm text-primary hover:underline">
                  {{ detail.repoName(row.source_repo_id) }}
                </router-link>
              </template>
              <template #cell-format="{ row }"><FormatBadge v-if="row.format" :format="row.format" size="sm" /></template>
              <template #cell-sha="{ row }"><ShortSha :sha="row.sha256" /></template>
              <template #cell-size="{ row }"><span class="tabular-nums">{{ formatBytes(row.size_bytes) }}</span></template>
              <template #cell-actions="{ row }">
                <RowActions
                  :actions="[{ key: 'remove', label: t('bundles.items.remove'), icon: TrashIcon, tone: 'danger' }]"
                  :subject="row.artifact_path"
                  @action="removeItem(row)"
                />
              </template>
            </ListTable>
          </div>
        </template>

        <template #panel-promotions>
          <BundlePromotionsTable :promotions="detail.promotions.value" />
        </template>

        <template #panel-security>
          <BundleScansTable :rows="scans.rows.value" :loading="scans.loading.value" />
        </template>

        <template #panel-verification>
          <BundleVerificationPanel :bundle="bundle" :name="name" :version="version" />
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
import { PlusIcon } from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import BundleItemsDrawer from '../components/bundles/BundleItemsDrawer.vue'
import BundleLifecycle from '../components/bundles/BundleLifecycle.vue'
import BundleMoreMenu, { type MoreMenuItem } from '../components/bundles/BundleMoreMenu.vue'
import BundlePromoteDrawer from '../components/bundles/BundlePromoteDrawer.vue'
import BundlePromotionsTable from '../components/bundles/BundlePromotionsTable.vue'
import BundleScansTable from '../components/bundles/BundleScansTable.vue'
import BundleSealDrawer from '../components/bundles/BundleSealDrawer.vue'
import BundleVerificationPanel from '../components/bundles/BundleVerificationPanel.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import FormatBadge from '../components/FormatBadge.vue'
import PageHeader from '../components/PageHeader.vue'
import ShortSha from '../components/ShortSha.vue'
import StatusBadge from '../components/StatusBadge.vue'
import AppTabs from '../components/ui/AppTabs.vue'
import RowActions from '../components/ui/RowActions.vue'
import { TrashIcon } from '@heroicons/vue/20/solid'
import ListTable, { type ListColumn } from '../components/ui/ListTable.vue'
import StateBlock from '../components/ui/StateBlock.vue'
import {
  addBundleItems,
  archiveBundle,
  deleteBundle,
  listBundleItems,
  removeBundleItem,
  type BundleItem,
} from '../api/bundles'
import { useBundleDetail } from '../composables/useBundleDetail'
import { useBundleDownloads } from '../composables/useBundleDownloads'
import { useBundleScans } from '../composables/useBundleScans'
import { useLocalList } from '../composables/useLocalList'
import type { TabDef } from '../composables/useUrlTab'
import {
  bundleStateLabel,
  bundleStateVariant,
  bundleStatus,
  NOTICE_TEXT,
  NOTICE_WARN,
  lifecycleSteps,
  promotableStages,
  type BundleStatus,
} from '../lib/bundles'
import { formatBytes } from '../lib/quota'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n'

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

const tabs = computed<TabDef[]>(() => {
  const critical = scans.critical.value
  return [
    { id: 'items', label: t('bundles.tab.items'), count: itemCount.value ?? undefined },
    { id: 'promotions', label: t('bundles.tab.promotions'), count: detail.promotions.value.length },
    {
      id: 'security',
      label: t('bundles.tab.security'),
      hidden: scans.rows.value.length === 0 && !scans.loading.value,
      flag: critical > 0 ? tp('bundles.scans.criticalFlag', { count: critical }) : undefined,
    },
    { id: 'verification', label: t('bundles.tab.verification') },
  ]
})

const itemColumns = computed<ListColumn[]>(() => [
  { key: 'path', label: t('bundles.items.column.path'), grow: true },
  { key: 'repo', label: t('bundles.items.column.repo') },
  { key: 'format', label: t('bundles.items.column.format'), dropOrder: 2 },
  { key: 'sha', label: t('bundles.items.column.sha'), dropOrder: 1 },
  { key: 'size', label: t('bundles.items.column.size'), align: 'end' },
  ...(bundle.value?.state === 'draft' ? [{ key: 'actions', label: t('common.actions'), actions: true }] : []),
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

const { downloadManifest, downloadSignature } = useBundleDownloads(() => name.value, () => version.value)

// Removing an item from a draft is reversible: the toast offers to put it back.
async function removeItem(item: BundleItem): Promise<void> {
  // The toast outlives the route: pin the bundle the item came out of.
  const bundleName = name.value
  const bundleVersion = version.value
  try {
    await removeBundleItem(bundleName, bundleVersion, item.sha256)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.items.removeFailed'))
    return
  }
  toasts.push({
    level: 'success',
    title: t('bundles.items.removed'),
    description: item.artifact_path,
    action: { label: t('bundles.items.undo'), run: () => void restoreItem(bundleName, bundleVersion, item) },
  })
  await items.refresh()
}

async function restoreItem(bundleName: string, bundleVersion: string, item: BundleItem): Promise<void> {
  try {
    await addBundleItems(bundleName, bundleVersion, [
      { source_repo_id: item.source_repo_id, sha256: item.sha256, artifact_path: item.artifact_path, format: item.format, size_bytes: item.size_bytes },
    ])
    if (bundleName === name.value && bundleVersion === version.value) await items.refresh()
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
