<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <div v-if="repo">
      <RepoHeader :name="repo" :repo="repoInfo" active="trash" />
      <p class="-mt-2 text-sm text-muted">{{ t('trash.descriptionRepo') }}</p>
    </div>
    <PageHeader v-else :title="t('trash.title')" :description="t('trash.description')" />

    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between gap-3 flex-wrap">
        <label class="flex items-center gap-2 text-sm text-muted">
          {{ t('trash.filter.kind') }}
          <select
            v-model="kind"
            class="px-2 py-1.5 border border-border-strong rounded-md text-sm bg-bg text-foreground"
            :aria-label="t('trash.filter.kind')"
          >
            <option value="">{{ t('trash.filter.all') }}</option>
            <option v-for="k in kindOptions" :key="k" :value="k">{{ kindLabel(k) }}</option>
          </select>
        </label>
        <div class="flex items-center gap-3">
          <span class="text-caption font-mono text-muted">{{ total }} {{ t('adm.common.total') }}</span>
          <AppButton
            v-if="repo && caps.empty && repoTotal > 0"
            variant="danger-outline"
            size="xs"
            :disabled="busyKey !== ''"
            @click="emptyOpen = true"
          >
            <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
            {{ t('trash.emptyTrash') }}
          </AppButton>
        </div>
      </div>

      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="items.length === 0 && kind !== '' && repoTotal > 0" class="px-4 py-10 text-center">
        <p class="text-foreground font-medium">{{ t('trash.emptyFiltered') }}</p>
        <p class="mt-1 text-sm text-muted">{{ tp('trash.emptyFilteredHint', { n: String(repoTotal) }) }}</p>
      </div>
      <div v-else-if="items.length === 0" class="px-4 py-10 text-center">
        <p class="text-foreground font-medium">{{ t('trash.empty') }}</p>
        <p class="mt-1 text-sm text-muted">{{ t('trash.emptyHint') }}</p>
        <DocsLink :url="docsUrl" class="mt-3">{{ t('docs.trash') }}</DocsLink>
      </div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-left">
          <thead>
            <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
              <th class="px-4 py-2 font-medium">{{ t('trash.column.type') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.name') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.version') }}</th>
              <th v-if="!repo" class="px-4 py-2 font-medium">{{ t('trash.column.repository') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.size') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.deleted') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.purge') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in items" :key="`${item.kind}:${item.id}`" class="border-b border-border/50 last:border-0">
              <td class="px-4 py-3 text-sm">
                <FormatBadge v-if="item.format && item.kind !== 'repository'" :format="item.format" size="sm" />
                <span class="ml-1 text-xs text-muted">{{ kindLabel(item.kind) }}</span>
              </td>
              <td class="px-4 py-3 max-w-xs">
                <button
                  type="button"
                  class="block max-w-full truncate text-left font-mono text-ui font-medium text-foreground hover:underline"
                  :title="`${item.name} — ${t('common.copy')}`"
                  @click="copyName(item.name)"
                >{{ item.name }}</button>
              </td>
              <td class="px-4 py-3 font-mono text-xs text-muted">{{ item.version || '—' }}</td>
              <td v-if="!repo" class="px-4 py-3 font-mono text-xs">{{ item.repository }}</td>
              <td class="px-4 py-3 text-xs text-muted">{{ item.kind === 'repository' ? '—' : formatBytesOrDash(item.size_bytes) }}</td>
              <td class="px-4 py-3 text-xs text-muted">
                <div class="font-mono">{{ formatDateTime(item.deleted_at) }}</div>
                <div v-if="item.deleted_by">{{ item.deleted_by }}</div>
              </td>
              <td class="px-4 py-3 text-xs text-muted">{{ purgeInText(item.purge_at) }}</td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-2">
                  <AppButton
                    v-if="restoreAllowed(item)"
                    variant="secondary"
                    size="xs"
                    :disabled="busyKey === keyOf(item)"
                    :aria-label="`${t('trash.restore')}: ${item.name}`"
                    @click="restore(item)"
                  >
                    <ArrowUturnLeftIcon class="w-3.5 h-3.5" aria-hidden="true" />
                    {{ t('trash.restore') }}
                  </AppButton>
                  <AppButton
                    v-if="caps.purge"
                    variant="danger-outline"
                    size="xs"
                    :disabled="busyKey === keyOf(item)"
                    :aria-label="`${t('trash.purge')}: ${item.name}`"
                    @click="purgeTarget = item"
                  >
                    <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
                    {{ t('trash.purge') }}
                  </AppButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pagination v-if="total > 0" :total="total" :page="page" :limit="PAGE_SIZE" @change="page = $event" />
    </div>

    <ConfirmDialog
      :open="purgeTarget !== null"
      :title="t('trash.purgeTitle')"
      :description="purgeDescription"
      :confirm-label="t('trash.purge')"
      :busy="busyKey !== ''"
      @confirm="doPurge"
      @close="purgeTarget = null"
    />

    <ConfirmDialog
      :open="emptyOpen"
      :title="t('trash.emptyTitle')"
      :description="tp('trash.emptyBody', { n: String(repoTotal), name: repo })"
      :confirm-label="t('trash.emptyTrash')"
      :busy="busyKey === 'empty'"
      @confirm="doEmpty"
      @close="emptyOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../components/PageHeader.vue'
import RepoHeader from '../components/RepoHeader.vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowUturnLeftIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import DocsLink from '../components/DocsLink.vue'
import FormatBadge from '../components/FormatBadge.vue'
import Pagination from '../components/Pagination.vue'
import { ApiError } from '../api/client'
import { getRepository, type Repository } from '../api/repositories'
import { emptyRepoTrash, listTrash, purgeTrash, restoreTrash, type TrashItem, type TrashKind } from '../api/trash'
import { formatDateTime } from '../lib/datetime'
import { emptyTrashResultText, purgeInText, trashCapabilities, type RepoPermissions } from '../lib/trash'
import { formatBytesOrDash } from '../lib/quota'
import { useSessionStore } from '../stores/session'
import { useDocsLink } from '../composables/useDocsLink'
import { useToastStore } from '../stores/toast'
import { t, tp, tDynamic } from '../i18n/en'

const PAGE_SIZE = 50

const route = useRoute()
const session = useSessionStore()
const toasts = useToastStore()
// The repository and the global trash share one docs page.
const docsUrl = useDocsLink('repo-trash')

// Per-repository view when the route carries :name; global admin view otherwise.
const repo = computed(() => (route.params.name ? String(route.params.name) : ''))

const items = ref<TrashItem[]>([])
const total = ref(0)
// Items in the whole repository trash, ignoring the kind filter.
const repoTotal = ref(0)
const page = ref(1)
const kind = ref('')
const loading = ref(false)
const loadError = ref('')
const busyKey = ref('')
const purgeTarget = ref<TrashItem | null>(null)
const emptyOpen = ref(false)
const permissions = ref<Partial<RepoPermissions> | null>(null)
const repoInfo = ref<Repository | null>(null)

const caps = computed(() => trashCapabilities(repo.value ? 'repo' : 'global', session.isAdmin, permissions.value))
// Repositories are only restorable through the global admin endpoint.
const restoreAllowed = (item: TrashItem): boolean => (item.kind === 'repository' ? session.isAdmin : caps.value.restore)
const purgeDescription = computed(() => {
  const target = purgeTarget.value
  if (!target) return ''
  return tp(target.kind === 'repository' ? 'trash.purgeRepoBody' : 'trash.purgeBody', { name: target.name })
})

async function copyName(name: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(name)
    toasts.success(t('common.copied'))
  } catch {
    // clipboard unavailable (insecure context): nothing to do
  }
}

// Loads the caller's permissions on the repository (drives which actions are offered).
async function loadPermissions(): Promise<void> {
  const name = repo.value
  permissions.value = null
  if (!name) return
  try {
    const r = await getRepository(name)
    if (name === repo.value) {
      repoInfo.value = r
      permissions.value = r.permissions ?? null
    }
  } catch {
    // the list request reports access problems; actions stay hidden without permissions
  }
}

const ALL_KINDS: TrashKind[] = [
  'repository', 'maven_artifact', 'ivy_artifact', 'sbt_artifact', 'npm_version', 'pypi_package',
  'nuget_package', 'go_module', 'cargo_crate', 'rubygems_gem', 'conda_package', 'conan_package',
  'helm_chart', 'raw_artifact', 'docker_manifest', 'docker_tag', 'rpm_package', 'deb_package',
]
const kindOptions = computed(() => (repo.value ? ALL_KINDS.filter((k) => k !== 'repository') : ALL_KINDS))

const keyOf = (item: TrashItem): string => `${item.kind}:${item.id}`
const kindLabel = (k: string): string => tDynamic(`trash.kind.${k}`)


// Guards against an older, slower response overwriting a newer one.
let loadSeq = 0

async function load(): Promise<void> {
  const seq = ++loadSeq
  loading.value = true
  loadError.value = ''
  try {
    const res = await listTrash({ page: page.value, limit: PAGE_SIZE, kind: kind.value }, repo.value || undefined)
    // "Empty trash" purges every kind, so its count must ignore the kind filter.
    let everything = res.total ?? 0
    if (repo.value && kind.value) {
      everything = (await listTrash({ page: 1, limit: 1 }, repo.value)).total ?? 0
    }
    if (seq !== loadSeq) return
    items.value = res.items ?? []
    total.value = res.total ?? 0
    repoTotal.value = everything
    // Last item of a page was removed: step back instead of showing an empty page.
    if (items.value.length === 0 && page.value > 1) page.value--
  } catch (err) {
    if (seq !== loadSeq) return
    items.value = []
    total.value = 0
    repoTotal.value = 0
    if (err instanceof ApiError && err.status === 403) loadError.value = t('trash.forbidden')
    else loadError.value = err instanceof Error ? err.message : t('trash.loadFailed')
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

async function restore(item: TrashItem): Promise<void> {
  busyKey.value = keyOf(item)
  try {
    // Repositories are only restorable through the global admin endpoint.
    await restoreTrash(item, item.kind === 'repository' ? undefined : repo.value || undefined)
    toasts.success(tp('trash.restored', { name: item.name }))
    await load()
  } catch (err) {
    toasts.error(t('trash.restoreFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    busyKey.value = ''
  }
}

async function doPurge(): Promise<void> {
  const item = purgeTarget.value
  if (!item) return
  busyKey.value = keyOf(item)
  try {
    await purgeTrash(item)
    toasts.success(tp('trash.purged', { name: item.name }))
    purgeTarget.value = null
    await load()
  } catch (err) {
    toasts.error(t('trash.purgeFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    busyKey.value = ''
  }
}

async function doEmpty(): Promise<void> {
  if (!repo.value) return
  busyKey.value = 'empty'
  try {
    const res = await emptyRepoTrash(repo.value)
    toasts.success(emptyTrashResultText(res.purged, res.remaining))
    emptyOpen.value = false
    await load()
  } catch (err) {
    toasts.error(t('trash.emptyFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    busyKey.value = ''
  }
}

watch(kind, () => {
  if (page.value === 1) void load()
  else page.value = 1
})
watch(page, () => void load())
watch(repo, () => {
  kind.value = ''
  page.value = 1
  void loadPermissions()
  void load()
})
onMounted(() => {
  void loadPermissions()
  void load()
})
</script>
