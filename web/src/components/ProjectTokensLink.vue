<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Project access tokens are managed under Settings, filtered to this project.
  This block only links there; the project has no token tab of its own.
-->
<template>
  <section class="rounded-lg border border-border bg-surface p-4 sm:p-5" :aria-label="t('projects.tokens.title')" data-testid="project-tokens-link">
    <h2 class="text-sm font-semibold text-foreground">{{ t('projects.tokens.title') }}</h2>
    <p class="mt-1 text-xs text-muted">{{ t('projects.tokens.hint') }}</p>
    <router-link
      :to="{ path: '/settings/tokens', query: { type: 'project', project } }"
      class="mt-3 inline-flex min-h-8 items-center text-sm font-medium text-primary hover:underline max-md:min-h-11"
    >{{ t('projects.tokens.manage') }}</router-link>
  </section>
</template>

<script setup lang="ts">
import { t } from '../i18n'

defineProps<{ project: string }>()
</script>
