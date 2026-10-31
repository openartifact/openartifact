<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The versions of a package a proxy exists for but its gate holds back, greyed, each with when it becomes available or why it is blocked. -->
<template>
  <section class="rounded-lg border border-border bg-surface p-4" aria-labelledby="held-back-title" data-testid="held-back-list">
    <h2 id="held-back-title" class="text-sm font-semibold text-foreground">{{ t('artifact.heldBack.title') }}</h2>
    <p class="mt-1 text-xs text-muted">{{ tp('artifact.heldBack.hint', { name: pkg }) }}</p>
    <ul class="mt-3 space-y-1" role="list">
      <li v-for="h in held" :key="h.version" class="flex flex-wrap items-baseline gap-x-3 text-sm text-muted">
        <span class="font-mono">{{ h.version }}</span>
        <span class="text-xs text-warning" :title="h.reasons[0]?.detail">{{ heldStatus(h) }}</span>
        <RelativeTime v-if="h.published_at" :value="h.published_at" class="ml-auto text-xs" />
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
import type { HeldBackVersion } from '../../api/curation'
import RelativeTime from '../ui/RelativeTime.vue'
import { heldStatus } from '../../lib/availability'
import { t, tp } from '../../i18n'

defineProps<{ pkg: string; held: HeldBackVersion[] }>()
</script>
