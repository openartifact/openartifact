<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The upstream and cache settings of a proxy repository: a read-only summary for everyone, and for
  whoever may PATCH the repository an "Edit proxy settings" form in its place.
-->
<template>
  <ProxySettingsForm v-if="editing" ref="form" :repo="repo" :can-block="canBlock" @saved="onSaved" @cancel="stopEditing" />
  <section v-else class="flex items-start justify-between gap-4 rounded-lg border border-border bg-surface px-4 py-3" :aria-label="t('repoDetail.proxy.title')">
    <dl class="grid min-w-0 grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-xs">
      <dt class="text-muted">{{ t('repoDetail.proxy.url') }}</dt>
      <dd class="min-w-0 break-all font-mono text-foreground">
        {{ repo.upstream_url || '—' }}
        <span v-if="repo.upstream_username" class="font-sans text-muted">· {{ tp('repoDetail.proxy.authenticatedAs', { user: repo.upstream_username }) }}</span>
      </dd>
      <dt class="text-muted">{{ t('repoDetail.proxy.cacheTtl') }}</dt>
      <dd class="text-foreground">{{ formatDuration(repo.cache_ttl_seconds) }}</dd>
      <dt class="text-muted">{{ t('repoDetail.proxy.negativeCacheTtl') }}</dt>
      <dd class="text-foreground">{{ formatDuration(repo.negative_cache_ttl_seconds) }}</dd>
      <dt class="text-muted" :title="t('repoDetail.proxy.maxStaleHint')">{{ t('repoDetail.proxy.maxStale') }}</dt>
      <dd class="text-foreground">{{ repo.max_stale_seconds ? formatDuration(repo.max_stale_seconds) : t('repoDetail.proxy.maxStaleOff') }}</dd>
      <dt class="text-muted" :title="t('repoDetail.proxy.minAgeHint')">{{ t('repoDetail.proxy.minAge') }}</dt>
      <dd class="text-foreground">{{ repo.min_age_hours ? tp('repoDetail.proxy.minAgeValue', { hours: String(repo.min_age_hours) }) : t('repoDetail.proxy.minAgeOff') }}</dd>
      <dt class="text-muted">{{ t('repoDetail.proxy.blocked') }}</dt>
      <dd :class="repo.blocked ? 'font-medium text-danger' : 'text-foreground'" data-testid="proxy-blocked">{{ repo.blocked ? t('repoDetail.proxy.blockedYes') : t('repoDetail.proxy.blockedNo') }}</dd>
    </dl>
    <AppButton v-if="canEdit" variant="secondary" size="sm" class="shrink-0" ref="editButton" data-testid="edit-proxy-settings" @click="startEditing">{{ t('repoDetail.proxy.edit') }}</AppButton>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, type ComponentPublicInstance } from 'vue'
import AppButton from '../AppButton.vue'
import ProxySettingsForm from './ProxySettingsForm.vue'
import { t, tp } from '../../i18n'
import { formatDuration } from '../../lib/duration'
import { useToastStore } from '../../stores/toast'
import { useSessionStore } from '../../stores/session'
import type { Repository } from '../../api/repositories'

const props = defineProps<{ repo: Repository }>()
const emit = defineEmits<{ saved: [repo: Repository] }>()

const session = useSessionStore()
const toasts = useToastStore()
const editing = ref(false)
const form = ref<ComponentPublicInstance | null>(null)
const editButton = ref<ComponentPublicInstance | null>(null)

// The button and the form replace each other, so the focused element disappears with every switch: put focus on
// the first field when the form opens and back on the button when it closes, or a keyboard user starts over at the top of the page.
async function startEditing(): Promise<void> {
  editing.value = true
  await nextTick()
  form.value?.$el.querySelector('input, select, textarea')?.focus()
}

async function stopEditing(): Promise<void> {
  editing.value = false
  await nextTick()
  editButton.value?.$el.focus()
}

// Global admins and maintainers/owners of the repository's project (repo.permissions.admin) may PATCH it;
// blocking a repository stays with the global administrators.
const canEdit = computed(() => session.isAdmin || props.repo.permissions?.admin === true)
const canBlock = computed(() => session.isAdmin)

function onSaved(updated: Repository): void {
  void stopEditing()
  toasts.success(t('repoDetail.proxy.saved'))
  emit('saved', updated)
}
</script>
