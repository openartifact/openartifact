<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Header shared by every page of a repository (detail, browse, graph, trash):
  the repository name with its format/type/visibility, page actions, and one
  section bar so all repository pages look and navigate the same way.
-->
<template>
  <div>
    <PageHeader :title="name" :crumbs="crumbs">
      <template v-if="repo" #meta>
        <FormatBadge :format="repo.format" />
        <span class="text-xs text-muted">{{ repo.repo_type || 'hosted' }}</span>
        <span class="text-xs text-muted" aria-hidden="true">·</span>
        <span class="inline-flex items-center gap-1 text-xs text-muted">
          <EyeIcon v-if="repo.public" class="w-3.5 h-3.5" aria-hidden="true" />
          <EyeSlashIcon v-else class="w-3.5 h-3.5" aria-hidden="true" />
          {{ repo.public ? t('repos.public') : t('repos.private') }}
        </span>
      </template>
      <template v-if="$slots.actions" #actions>
        <slot name="actions" />
      </template>
    </PageHeader>

    <nav
      :aria-label="t('repoDetail.tabs')"
      class="-mt-1 mb-6 flex items-center gap-1 overflow-x-auto border-b border-border"
    >
      <router-link
        v-for="s in sections"
        :key="s.id"
        :to="s.to"
        :aria-current="s.id === active ? 'page' : undefined"
        class="relative h-10 px-3 inline-flex items-center gap-1.5 text-sm font-medium shrink-0 transition-colors"
        :class="s.id === active ? 'text-foreground' : 'text-muted hover:text-foreground'"
      >
        {{ s.label }}
        <span
          v-if="s.count !== undefined"
          class="text-micro font-mono px-1.5 py-0.5 rounded"
          :class="s.id === active ? 'bg-primary-soft text-primary' : 'bg-surface-hover text-muted'"
        >{{ s.count }}</span>
        <span
          v-if="s.id === active"
          class="absolute left-2 right-2 -bottom-px h-[2px] bg-primary rounded-full"
          aria-hidden="true"
        />
      </router-link>
    </nav>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { RouteLocationRaw } from 'vue-router'
import { EyeIcon, EyeSlashIcon } from '@heroicons/vue/24/outline'
import PageHeader from './PageHeader.vue'
import FormatBadge from './FormatBadge.vue'
import type { Repository } from '../api/repositories'
import type { Crumb } from '../lib/crumbs'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n/en'

export type RepoSection = 'main' | 'browse' | 'graph' | 'trash' | 'configuration' | 'cleanup'

const props = defineProps<{
  name: string
  repo: Repository | null
  active: RepoSection
  /** Item count shown on the main section (packages, cached items or members). */
  count?: number
  crumbs?: Crumb[]
}>()

const session = useSessionStore()

const sections = computed(() => {
  const base = `/repos/${encodeURIComponent(props.name)}`
  const type = props.repo?.repo_type ?? 'hosted'
  const list: Array<{ id: RepoSection; label: string; to: RouteLocationRaw; count?: number }> = []

  const mainLabel =
    type === 'group' ? t('repoDetail.tab.members')
    : type === 'proxy' ? t('repoDetail.tab.cached')
    : t('repoDetail.tab.packages')
  list.push({ id: 'main', label: mainLabel, to: base, count: props.count })

  if (type !== 'group') {
    list.push(
      { id: 'browse', label: t('browser.title'), to: `${base}/browse` },
      { id: 'graph', label: t('graph.title'), to: `${base}/graph` },
    )
  }
  // Repo-scoped permissions come from the server, so non-admin principals with a delete grant also see the trash.
  if (props.repo?.permissions?.delete) {
    list.push({ id: 'trash', label: t('trash.title'), to: `${base}/trash` })
  }
  if (session.isAdmin) {
    list.push(
      { id: 'configuration', label: t('repoDetail.tab.configuration'), to: { path: base, query: { tab: 'configuration' } } },
      { id: 'cleanup', label: t('repoDetail.tab.cleanup'), to: { path: base, query: { tab: 'cleanup' } } },
    )
  }
  return list
})
</script>
