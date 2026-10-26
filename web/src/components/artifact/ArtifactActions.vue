<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Actions on the shown version: download, permalink, quarantine / release and delete.
  Quarantine and release run at once and are reversible from the toast; deleting asks
  first because it removes the version for clients.
-->
<template>
  <div v-if="entry" class="flex items-center gap-2">
    <AppButton v-if="primary" variant="secondary" :loading="downloading" @click="download">
      <ArrowDownTrayIcon class="h-4 w-4 text-muted" aria-hidden="true" />
      {{ t('artifact.action.download') }}
    </AppButton>

    <Menu as="div" class="relative">
      <MenuButton
        class="inline-flex h-9 w-9 items-center justify-center rounded-lg border border-border text-muted hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11 max-md:w-11"
        :aria-label="t('artifact.action.more')"
        data-testid="artifact-menu"
      >
        <EllipsisHorizontalIcon class="h-5 w-5" aria-hidden="true" />
      </MenuButton>
      <MenuItems class="absolute right-0 z-30 mt-2 w-60 origin-top-right rounded-md border border-border bg-surface py-1 shadow-lg focus:outline-none">
        <MenuItem v-slot="{ active }">
          <button type="button" :class="[ITEM, active && 'bg-surface-hover']" @click="copyPermalink">
            <LinkIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />{{ t('artifact.action.permalink') }}
          </button>
        </MenuItem>
        <MenuItem v-if="ctx.caps.value.maintain" v-slot="{ active }">
          <button type="button" :class="[ITEM, active && 'bg-surface-hover']" data-testid="action-quarantine" @click="toggleQuarantine">
            <component :is="quarantined ? ArrowUturnLeftIcon : LockClosedIcon" class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />
            {{ quarantined ? t('artifact.action.release') : t('artifact.action.quarantine') }}
          </button>
        </MenuItem>
        <MenuItem v-if="canDelete" v-slot="{ active }">
          <button type="button" :class="[ITEM, 'text-danger', active && 'bg-surface-hover']" data-testid="action-delete" @click="confirmOpen = true">
            <TrashIcon class="h-4 w-4 shrink-0" aria-hidden="true" />{{ t('artifact.action.delete') }}
          </button>
        </MenuItem>
      </MenuItems>
    </Menu>

    <ConfirmDialog
      :open="confirmOpen"
      :title="tp('artifact.delete.title', { name: pkgName, version: entry.version })"
      :description="deleteDescription"
      :confirm-label="t('artifact.action.delete')"
      :busy-label="t('artifact.delete.busy')"
      :busy="deleting"
      @confirm="remove"
      @close="confirmOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Menu, MenuButton, MenuItem, MenuItems } from '@headlessui/vue'
import { ArrowDownTrayIcon, ArrowUturnLeftIcon, EllipsisHorizontalIcon, LinkIcon, LockClosedIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import { setVersionQuarantine } from '../../api/artifact'
import { deletePackage, downloadPackage } from '../../api/repositories'
import { useArtifactContext } from '../../composables/useArtifact'
import { artifactPath } from '../../lib/artifactPath'
import { primaryFile } from '../../lib/artifactVersions'
import { supportsPackageDelete } from '../../lib/formats'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n/en'

const ITEM = 'flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-foreground max-md:min-h-11'

const ctx = useArtifactContext()
const router = useRouter()
const toasts = useToastStore()

const entry = computed(() => ctx.entry.value)
const pkgName = computed(() => ctx.pkgName.value)
const primary = computed(() => (entry.value ? primaryFile(entry.value) : undefined))
const quarantined = computed(() => ctx.summary.value?.quarantined === true)
const canDelete = computed(
  () => ctx.caps.value.remove && ctx.repository.value?.repo_type === 'hosted' && supportsPackageDelete(ctx.format.value),
)
const fileIds = computed(() => entry.value?.files.map((f) => f.id) ?? [])

const downloading = ref(false)
async function download(): Promise<void> {
  const file = primary.value
  if (!file) return
  downloading.value = true
  try {
    await downloadPackage(ctx.repoName.value, file)
  } catch (err) {
    toasts.error(t('artifact.action.downloadFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    downloading.value = false
  }
}

async function copyPermalink(): Promise<void> {
  if (!entry.value) return
  const url = new URL(artifactPath(ctx.repoName.value, pkgName.value, entry.value.version), window.location.origin).href
  try {
    await navigator.clipboard.writeText(url)
    toasts.success(t('artifact.action.permalinkCopied'))
  } catch {
    toasts.error(t('artifact.action.permalinkFailed'))
  }
}

// Both directions apply at once; the toast carries the way back.
async function applyQuarantine(flag: boolean, undo: boolean): Promise<void> {
  const current = entry.value
  if (!current) return
  try {
    const changed = await setVersionQuarantine(ctx.repoName.value, fileIds.value, flag)
    if (changed === 0) {
      toasts.warning(t(flag ? 'artifact.quarantine.nothingScanned' : 'artifact.quarantine.nothingQuarantined'))
      return
    }
    await ctx.reloadSecurity()
    if (undo) {
      toasts.success(tp(flag ? 'artifact.quarantine.done' : 'artifact.quarantine.released', { version: current.version }))
      return
    }
    toasts.push({
      level: 'success',
      title: tp(flag ? 'artifact.quarantine.done' : 'artifact.quarantine.released', { version: current.version }),
      description: t(flag ? 'artifact.quarantine.doneHint' : 'artifact.quarantine.releasedHint'),
      action: { label: t('artifact.undo'), run: () => void applyQuarantine(!flag, true) },
      timeoutMs: 10000,
    })
  } catch (err) {
    toasts.error(t('artifact.quarantine.failed'), err instanceof Error ? err.message : undefined)
  }
}

function toggleQuarantine(): void {
  void applyQuarantine(!quarantined.value, false)
}

const confirmOpen = ref(false)
const deleting = ref(false)

const deleteDescription = computed(() => {
  const days = ctx.repository.value?.trash_retention_days
  if (days === 0) return t('artifact.delete.permanent')
  if (typeof days === 'number') return tp('artifact.delete.trashDays', { repo: ctx.repoName.value, days: String(days) })
  return tp('artifact.delete.trash', { repo: ctx.repoName.value })
})

async function remove(): Promise<void> {
  const current = entry.value
  if (!current) return
  deleting.value = true
  try {
    await Promise.all(fileIds.value.map((id) => deletePackage(ctx.repoName.value, id)))
  } catch (err) {
    toasts.error(t('artifact.delete.failed'), err instanceof Error ? err.message : undefined)
    deleting.value = false
    return
  }
  deleting.value = false
  confirmOpen.value = false
  const repo = ctx.repoName.value
  const keeps = ctx.repository.value?.trash_retention_days !== 0
  toasts.push({
    level: 'success',
    title: tp('artifact.delete.done', { name: pkgName.value, version: current.version }),
    action: keeps ? { label: t('artifact.delete.openTrash'), run: () => void router.push({ path: `/repos/${encodeURIComponent(repo)}`, query: { tab: 'trash' } }) } : undefined,
    timeoutMs: 8000,
  })
  const remaining = ctx.versions.value.filter((v) => v.version !== current.version)
  if (remaining.length === 0) {
    await router.push({ path: `/repos/${encodeURIComponent(repo)}`, query: { tab: 'packages' } })
    return
  }
  await router.push(artifactPath(repo, pkgName.value))
  await ctx.reload()
}
</script>
