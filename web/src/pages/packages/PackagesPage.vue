<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('search.title')" :description="t('search.description')" />

    <SearchControls :list="list" :mode="mode" :auto-checksum="autoChecksum" :repos="repoNames" @update:mode="setMode" />

    <div v-if="plan.kind === 'idle'" class="rounded-lg border border-border bg-surface" data-testid="search-start">
      <StateBlock variant="empty" :title="t('search.start.title')" :hint="examples.length > 0 ? t('search.start.hintExamples') : t('search.start.hint')">
        <template v-if="examples.length > 0" #action>
          <router-link
            v-for="e in examples"
            :key="e.name"
            :to="{ path: '/packages', query: { q: e.name } }"
            class="max-w-full rounded-md border border-border px-3 py-1.5 text-left hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:min-h-11"
            data-testid="search-example"
          >
            <code class="block truncate font-mono text-sm text-foreground">{{ e.name }}</code>
            <span class="block truncate text-xs text-muted">{{ e.repository }}</span>
          </router-link>
        </template>
        <template v-else #action>
          <p class="w-full text-sm text-muted">{{ t('search.start.publish') }}</p>
          <div class="flex max-w-full items-start gap-1 rounded-md border border-border bg-surface-hover pl-3 text-left">
            <code class="min-w-0 overflow-x-auto whitespace-pre py-2 font-mono text-xs text-foreground">{{ t('search.start.publishCommand') }}</code>
            <CopyButton :text="t('search.start.publishCommand')" :label="t('ui.state.copyCommand')" />
          </div>
          <AppButton variant="secondary" to="/#connect">{{ t('search.start.connect') }}</AppButton>
        </template>
      </StateBlock>
    </div>
    <section v-if="plan.kind === 'idle' && recent.length > 0" class="mt-6" aria-labelledby="pk-recent">
      <h2 id="pk-recent" class="mb-2 text-base font-semibold">{{ t('search.recent.title') }}</h2>
      <RecentPackages :items="recent" />
    </section>

    <div v-else-if="plan.kind === 'invalid-checksum'" class="rounded-lg border border-border bg-surface" data-testid="bad-checksum">
      <StateBlock variant="empty" :title="t('search.badChecksum.title')" :hint="t('search.badChecksum.hint')">
        <template #action>
          <AppButton variant="secondary" @click="setMode('name')">{{ t('search.badChecksum.name') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <SearchResults v-else-if="plan.kind !== 'idle'" :list="list" :mode="mode" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { listRecentPackages, listRepositoryPage, type RecentPackage } from '../../api/dashboard'
import AppButton from '../../components/AppButton.vue'
import PageHeader from '../../components/PageHeader.vue'
import RecentPackages from '../../components/packages/RecentPackages.vue'
import SearchControls from '../../components/packages/SearchControls.vue'
import SearchResults from '../../components/packages/SearchResults.vue'
import CopyButton from '../../components/ui/CopyButton.vue'
import StateBlock from '../../components/ui/StateBlock.vue'
import { useSearch } from '../../composables/useSearch'
import { t } from '../../i18n'

const { list, plan, mode, autoChecksum, setMode } = useSearch()

// Both lists are conveniences (repository filter, start screen): a failure leaves them empty.
const repoNames = ref<string[]>([])
const recent = ref<RecentPackage[]>([])
// Names to try, taken from what was published last: the search starts from something that exists.
const examples = computed(() => [...new Map(recent.value.map((p) => [p.name, p])).values()].slice(0, 3))
onMounted(() => {
  listRepositoryPage({ sort: 'name', limit: 100 })
    .then((p) => void (repoNames.value = p.repositories.map((r) => r.name)))
    .catch(() => undefined)
  listRecentPackages({ mine: false, limit: 5 })
    .then((p) => void (recent.value = p))
    .catch(() => undefined)
})
</script>
