<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<script setup lang="ts">
// The async wrapper forwards the tab's props to this placeholder; keep them off the DOM.
defineOptions({ inheritAttrs: false })
</script>

<template>
  <div class="animate-pulse space-y-4" role="status" aria-busy="true" data-testid="tab-skeleton">
    <div class="h-6 w-48 rounded bg-surface-hover" />
    <div class="h-4 w-2/3 rounded bg-surface-hover" />
    <div class="h-48 rounded-xl border border-border bg-surface-hover/50" />
  </div>
</template>
