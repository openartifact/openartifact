<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('search.title')" />

    <!-- Search Input -->
    <div class="w-full flex items-center gap-3 px-3 h-11 rounded-lg bg-surface border border-border-strong focus-within:border-primary focus-within:ring-2 focus-within:ring-primary/30 transition-colors">
      <MagnifyingGlassIcon class="w-5 h-5 text-muted flex-shrink-0" aria-hidden="true" />
      <input
        v-model="query"
        type="text"
        :placeholder="t('search.placeholder')"
        :aria-label="t('search.title')"
        class="flex-1 bg-transparent text-foreground placeholder:text-muted text-sm focus-visible:outline-none"
        @input="onInput"
        @keydown.enter="doSearch"
      />
      <AppButton v-if="query" variant="ghost" size="xs" :aria-label="t('search.clear')" @click="clearSearch">
        <XMarkIcon class="w-4 h-4" aria-hidden="true" />
      </AppButton>
    </div>

    <!-- Results Section -->
    <div v-if="error" class="mt-6 rounded-xl border border-danger/30 bg-surface p-6 text-sm text-danger" role="alert">
      {{ error }}
    </div>
    <div v-else-if="searched" class="mt-6 space-y-6">
      <!-- Repositories Results -->
      <div v-if="repoResults.length" class="bg-surface rounded-xl border border-border overflow-hidden">
        <div class="px-5 py-3 border-b border-border flex items-center justify-between">
          <span class="text-sm font-semibold">{{ t('search.results.repositories') }}</span>
          <span class="text-xs bg-primary-soft text-primary rounded-full px-2 py-0.5">{{ repoResults.length }}</span>
        </div>
        <div>
          <router-link
            v-for="(repo, idx) in repoResults"
            :key="repo.name"
            :to="`/repos/${encodeURIComponent(repo.name)}`"
            :class="[
              'px-5 py-3 hover:bg-surface-hover transition-colors flex items-center gap-3',
              idx > 0 ? 'border-t border-border' : ''
            ]"
          >
            <span class="font-medium text-foreground">{{ repo.name }}</span>
            <FormatBadge :format="repo.format" />
            <span v-if="repo.description" class="text-sm text-muted truncate ml-auto max-w-xs">{{ repo.description }}</span>
          </router-link>
        </div>
      </div>

      <!-- Packages Results -->
      <div v-if="packageResults.length" class="bg-surface rounded-xl border border-border overflow-hidden">
        <div class="px-5 py-3 border-b border-border flex items-center justify-between">
          <span class="text-sm font-semibold">{{ t('search.results.packages') }}</span>
          <span class="text-xs bg-primary-soft text-primary rounded-full px-2 py-0.5">{{ packageResults.length }}</span>
        </div>
        <div>
          <router-link
            v-for="(pkg, idx) in packageResults"
            :key="`${pkg.repository}:${pkg.format}:${pkg.name}:${pkg.version}`"
            :to="`/repos/${encodeURIComponent(pkg.repository)}/packages/${encodeURIComponent(pkg.format)}/${encodeURIComponent(pkg.name)}`"
            :class="[
              'px-5 py-3 hover:bg-surface-hover transition-colors flex items-center gap-3',
              idx > 0 ? 'border-t border-border' : ''
            ]"
          >
            <span class="font-medium text-foreground">{{ pkg.name }}</span>
            <span class="font-mono text-xs text-muted">{{ pkg.version }}</span>
            <FormatBadge :format="pkg.format" />
            <span class="text-xs text-muted">{{ pkg.repository }}</span>
            <span v-if="pkg.description" class="text-sm text-muted truncate">{{ pkg.description }}</span>
          </router-link>
        </div>
      </div>

      <!-- No Results -->
      <EmptyState
        v-if="!repoResults.length && !packageResults.length"
        :title="t('search.empty')"
        :description="t('search.emptyHint')"
      />
    </div>

    <!-- Initial State -->
    <div v-else class="mt-6 flex flex-col items-center">
      <MagnifyingGlassIcon class="w-8 h-8 text-muted mt-8" aria-hidden="true" />
      <EmptyState :title="t('search.title')" :description="t('search.initialHint')" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { useRoute } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'
import { useSearch } from '../composables/useSearch'
import AppButton from '../components/AppButton.vue'
import EmptyState from '../components/EmptyState.vue'
import FormatBadge from '../components/FormatBadge.vue'
import { t } from '../i18n/en'
import { MagnifyingGlassIcon, XMarkIcon } from '@heroicons/vue/24/outline'

const route = useRoute()
const { query, repoResults, packageResults, searched, error, scheduleSearch, execute, clear } = useSearch()

// /search?q=... (the command palette's "view all results") runs that search,
// on arrival and when only the query string changes.
watch(
  () => route.query.q,
  (q) => {
    const text = typeof q === 'string' ? q.trim() : ''
    if (text && text !== query.value.trim()) {
      query.value = text
      void execute()
    }
  },
  { immediate: true },
)

function onInput() {
  scheduleSearch()
}

function doSearch() {
  execute()
}

function clearSearch() {
  clear()
}
</script>
