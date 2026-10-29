<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  A bordered card with a header row: the title (with an optional description
  and a `badge` slot beside it) and the card's actions (`action` slot). The
  default slot is the body.
-->
<template>
  <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="title">
    <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
      <div class="min-w-0 mr-auto">
        <div class="flex items-center gap-2">
          <h2 class="text-sm font-semibold text-foreground">{{ title }}</h2>
          <slot name="badge" />
        </div>
        <p v-if="description" class="text-caption text-muted">{{ description }}</p>
      </div>
      <slot name="action" />
    </div>
    <slot />
  </section>
</template>

<script setup lang="ts">
defineProps<{ title: string; description?: string }>()
</script>
