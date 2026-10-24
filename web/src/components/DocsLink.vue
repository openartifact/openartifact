<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Muted link into the published docs, opened in a new tab. `url` comes from
  `useDocsLink`; when it is null or empty (docs links disabled) nothing renders.
-->
<template>
  <a
    v-if="url"
    :href="url"
    target="_blank"
    rel="noopener"
    data-testid="docs-link"
    class="inline-flex items-center gap-1 text-sm text-muted underline-offset-2 hover:text-foreground hover:underline"
  >
    <BookOpenIcon class="h-4 w-4 shrink-0" aria-hidden="true" />
    <slot>{{ t('docs.readMore') }}</slot>
  </a>
</template>

<script setup lang="ts">
import { BookOpenIcon } from '@heroicons/vue/24/outline'
import { t } from '../i18n/en'

defineProps<{ url?: string | null }>()
</script>
