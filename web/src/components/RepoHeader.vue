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
        <RepoMeta :repo="repo" />
      </template>
      <template v-if="$slots.actions" #actions>
        <slot name="actions" />
      </template>
    </PageHeader>

    <SectionTabs :items="sections" :active="active" :label="t('repoDetail.tabs')" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import PageHeader from './PageHeader.vue'
import SectionTabs, { type SectionTab } from './SectionTabs.vue'
import RepoMeta from './RepoMeta.vue'
import type { Repository } from '../api/repositories'
import type { Crumb } from '../lib/crumbs'
import { showsFederation } from '../lib/federation'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n'

export type RepoSection = 'main' | 'browse' | 'graph' | 'trash' | 'configuration' | 'cleanup' | 'federation'

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
  const list: SectionTab[] = []

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
  if (session.isAdmin || props.repo?.permissions?.admin) {
    list.push(
      { id: 'configuration', label: t('repoDetail.tab.configuration'), to: { path: base, query: { tab: 'configuration' } } },
      { id: 'cleanup', label: t('repoDetail.tab.cleanup'), to: { path: base, query: { tab: 'cleanup' } } },
    )
    if (props.repo && showsFederation(props.repo)) {
      list.push({ id: 'federation', label: t('federation.tab'), to: { path: base, query: { tab: 'federation' } } })
    }
  }
  return list
})
</script>
