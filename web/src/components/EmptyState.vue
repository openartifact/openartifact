<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex flex-col items-center justify-center py-16 px-4 text-center">
    <h3 class="text-base font-semibold text-foreground mb-1">{{ title }}</h3>
    <p v-if="description" class="text-sm text-muted max-w-sm">{{ description }}</p>
    <code
      v-if="snippet"
      data-testid="empty-snippet"
      class="mt-3 max-w-full overflow-x-auto whitespace-pre rounded-md border border-border bg-surface-hover px-3 py-2 text-left text-xs font-mono text-foreground"
    >{{ snippet }}</code>
    <AppButton v-if="actionLabel && actionTo" class="mt-4" :to="actionTo">{{ actionLabel }}</AppButton>
    <div v-if="$slots.default" class="mt-4"><slot /></div>
    <DocsLink v-if="docsUrl" :url="docsUrl" class="mt-3">{{ docsLabel ?? t('docs.readMore') }}</DocsLink>
  </div>
</template>

<script setup lang="ts">
import AppButton from './AppButton.vue'
import DocsLink from './DocsLink.vue'
import { t } from '../i18n/en'

defineProps<{
  title: string
  description?: string
  actionLabel?: string
  actionTo?: string
  /** A short command or config line that gets the user out of the empty state. */
  snippet?: string
  /** Docs page for this state (from `useDocsLink`); the link is omitted when null or empty. */
  docsUrl?: string | null
  /** Link text; defaults to "Read the documentation". */
  docsLabel?: string
}>()
</script>
