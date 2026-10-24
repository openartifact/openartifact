<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <header class="mb-5 flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <nav v-if="trail.length > 1" :aria-label="t('shell.breadcrumbs')" class="mb-1">
        <ol class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-muted">
          <li
            v-for="(crumb, i) in trail"
            :key="i"
            class="flex min-w-0 items-center gap-1.5"
          >
            <ChevronRightIcon v-if="i > 0" class="h-3 w-3 shrink-0" aria-hidden="true" />
            <span
              v-if="i === trail.length - 1"
              aria-current="page"
              class="truncate font-medium text-foreground"
            >{{ crumb.label }}</span>
            <router-link
              v-else-if="crumb.to"
              :to="crumb.to"
              class="max-w-[20ch] truncate hover:text-foreground hover:underline sm:max-w-none"
            >{{ crumb.label }}</router-link>
            <span v-else class="truncate">{{ crumb.label }}</span>
          </li>
        </ol>
      </nav>
      <div class="flex flex-wrap items-center gap-2.5">
        <h1 class="min-w-0 break-words text-2xl font-bold tracking-tight">{{ title }}</h1>
        <slot name="meta" />
        <a
          v-if="helpUrl"
          :href="helpUrl"
          target="_blank"
          rel="noopener"
          :aria-label="t('docs.helpPage')"
          :title="t('docs.helpPage')"
          data-testid="page-help"
          class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-muted transition-colors hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          <QuestionMarkCircleIcon class="h-5 w-5" aria-hidden="true" />
        </a>
      </div>
      <p v-if="description" class="mt-1 max-w-3xl text-sm text-muted">{{ description }}</p>
    </div>
    <div v-if="$slots.actions" class="flex flex-wrap items-center gap-2">
      <slot name="actions" />
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { ChevronRightIcon } from '@heroicons/vue/20/solid'
import { QuestionMarkCircleIcon } from '@heroicons/vue/24/outline'
import { resolveCrumbs, type Crumb } from '../lib/crumbs'
import { useDocsLink } from '../composables/useDocsLink'
import { t } from '../i18n/en'

const props = defineProps<{
  title: string
  description?: string
  /** Overrides the trail declared in the route's `meta.crumbs`. */
  crumbs?: Crumb[]
}>()

const route = useRoute()
const trail = computed(() => props.crumbs ?? resolveCrumbs(route))
const helpUrl = useDocsLink(() => route.name)
</script>
