<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Why a package version cannot be downloaded from its repository: one row per hold
  (minimum age with a countdown, curation rule, quarantine, license policy, blocked
  repository). Renders nothing for an available version and for a repository that
  cannot answer (a group), so it can sit on every package and version page.
-->
<template>
  <section v-if="verdict && !verdict.available" class="rounded-lg border border-warning/30 bg-warning-soft p-4" aria-labelledby="availability-title" data-testid="availability-panel">
    <h2 id="availability-title" class="flex items-center gap-2 text-sm font-semibold text-warning">
      <ExclamationTriangleIcon class="h-4 w-4" aria-hidden="true" />{{ t('artifact.availability.title') }}
    </h2>
    <p class="mt-1 text-sm text-foreground">{{ tp('artifact.availability.intro', { name: pkg, version }) }}</p>
    <ul class="mt-3 space-y-2" role="list">
      <li v-for="(reason, i) in verdict.reasons" :key="i" class="text-sm" data-testid="availability-reason">
        <span class="mr-2 rounded-full border border-warning/30 px-2 py-0.5 text-xs text-warning">{{ sourceLabel(reason.source) }}</span>
        <span class="text-foreground">{{ reasonText(reason) }}</span>
        <span v-if="reason.until" class="ml-2 font-medium tabular-nums text-foreground" :title="tp('artifact.availability.untilTitle', { date: formatDateTime(reason.until) })" data-testid="availability-until">
          <time :datetime="reason.until">{{ tp('artifact.availability.until', { when: timeUntil(reason.until) }) }}</time>
        </span>
        <router-link v-if="reason.source === 'curation' && canManage" :to="{ path: `/repos/${encodeURIComponent(repo)}`, query: { tab: 'settings' } }" class="ml-2 text-primary hover:underline" data-testid="availability-rule-link">
          {{ t('artifact.availability.rule') }}
        </router-link>
      </li>
    </ul>
    <p v-if="verdict.published_at" class="mt-3 text-xs text-muted">{{ tp('artifact.availability.published', { when: formatDateTime(verdict.published_at) }) }}</p>
  </section>
  <p v-else-if="verdict && verdict.min_age === 'unknown'" class="rounded-lg border border-border px-4 py-3 text-xs text-muted" data-testid="availability-unknown-age">
    {{ t('artifact.availability.unknownAge') }}
  </p>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/20/solid'
import { getAvailability, type AvailabilityResponse } from '../../api/curation'
import { formatDateTime, timeUntil } from '../../lib/datetime'
import { reasonText, sourceLabel } from '../../lib/availability'
import { t, tp } from '../../i18n'

const props = defineProps<{
  repo: string
  pkg: string
  version: string
  /** A proxy or hosted repository; a group has no verdict of its own. */
  enabled: boolean
  /** The viewer may open the repository's settings (where the curation rules are). */
  canManage: boolean
}>()

const verdict = ref<AvailabilityResponse | null>(null)

let generation = 0
watch(
  () => [props.repo, props.pkg, props.version, props.enabled] as const,
  async ([repo, pkg, version, enabled]) => {
    const mine = ++generation
    verdict.value = null
    if (!enabled) return
    try {
      const res = await getAvailability(repo, pkg, version)
      if (mine === generation) verdict.value = res
    } catch {
      // The explanation is an extra: without it the page shows no panel.
    }
  },
  { immediate: true },
)
</script>
