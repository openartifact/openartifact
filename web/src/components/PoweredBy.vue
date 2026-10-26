<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Attribution of the software that runs this instance. Not configurable and
  independent of the product name: the only branding value it reads is the
  source URL (the AGPL section 13 offer), and that falls back to the upstream
  repository when empty or unusable.
-->
<template>
  <p class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-caption text-muted" data-testid="powered-by">
    <span data-testid="powered-by-text">{{ label }}</span>
    <span aria-hidden="true">·</span>
    <span>{{ ATTRIBUTION.licenseShort }}</span>
    <span aria-hidden="true">·</span>
    <a
      :href="sourceHref"
      target="_blank"
      rel="noopener noreferrer"
      data-testid="source-link"
      class="underline underline-offset-2 hover:text-foreground max-md:inline-flex max-md:min-h-11 max-md:items-center"
    >{{ ATTRIBUTION.sourceCode }}</a>
  </p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useBrandingStore } from '../stores/branding'
import { useAppVersion, formatVersion } from '../composables/useAppVersion'
import { DEFAULT_SOURCE_URL, safeUrl } from '../lib/branding'
import { ATTRIBUTION } from '../lib/attribution'

const store = useBrandingStore()
const version = useAppVersion()

const sourceHref = computed(() => safeUrl(store.sourceUrl) || DEFAULT_SOURCE_URL)
const label = computed(() =>
  version.value ? `${ATTRIBUTION.poweredBy} ${formatVersion(version.value)}` : ATTRIBUTION.poweredBy,
)
</script>
