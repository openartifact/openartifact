<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Loading placeholder of a card of rows: an optional header bar (`header` = its size classes) and `rows`
  rows whose cells come from the default slot. `actions` ends each row with the three row-action buttons.
-->
<template>
  <div class="bg-surface rounded-xl border border-border overflow-hidden">
    <div v-if="header" class="px-5 py-4 border-b border-border">
      <div class="rounded skel" :class="header" />
    </div>
    <div v-for="i in rows" :key="i" class="flex items-center gap-4 px-5 py-4 border-b border-border last:border-0">
      <slot />
      <div v-if="actions" class="flex gap-1">
        <div v-for="n in 3" :key="n" class="w-7 h-7 rounded-md skel" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ header?: string; rows?: number; actions?: boolean }>(), { header: '', rows: 3, actions: false })
</script>
