<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <h1 class="text-2xl font-semibold text-foreground">{{ t('search.title') }}</h1>

    <!-- Search Input -->
    <div class="w-full mt-4 flex items-center gap-3 px-5 py-3 rounded-full bg-surface border border-border focus-within:border-primary/50 focus-within:ring-2 focus-within:ring-primary/20 transition-all">
      <MagnifyingGlassIcon class="w-5 h-5 text-muted flex-shrink-0" aria-hidden="true" />
      <input
        v-model="query"
        type="text"
        :placeholder="t('search.placeholder')"
        :aria-label="t('search.title')"
        class="flex-1 bg-transparent text-foreground placeholder:text-muted outline-none text-sm"
        @input="onInput"
        @keydown.enter="doSearch"
      />
      <button
        v-if="query"
        :aria-label="t('search.clear')"
        @click="clearSearch"
        class="flex-shrink-0 text-muted hover:text-foreground transition-colors"
      >
        <XMarkIcon class="w-5 h-5" aria-hidden="true" />
      </button>
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
          <span class="text-xs bg-primary/10 text-primary rounded-full px-2 py-0.5">{{ repoResults.length }}</span>
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
          <span class="text-xs bg-primary/10 text-primary rounded-full px-2 py-0.5">{{ packageResults.length }}</span>
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
      <p v-if="!repoResults.length && !packageResults.length" class="text-sm text-muted">
        {{ t('search.empty') }} {{ t('search.emptyHint') }}
      </p>
    </div>

    <!-- Initial State -->
    <p v-else class="mt-6 text-sm text-muted">{{ t('search.initialHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useSearch } from '../composables/useSearch'
import FormatBadge from '../components/FormatBadge.vue'
import { t } from '../i18n/en'
import { MagnifyingGlassIcon, XMarkIcon } from '@heroicons/vue/24/outline'

const { query, repoResults, packageResults, searched, error, scheduleSearch, execute, clear } = useSearch()

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
