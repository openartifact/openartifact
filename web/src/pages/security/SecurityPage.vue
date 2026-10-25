<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Stand-in for the unified findings page: renders the existing scan page picked by `?type`. -->
<template>
  <component :is="page" />
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { useRoute } from 'vue-router'
import PageLoadingFallback from '../../components/PageLoadingFallback.vue'

const route = useRoute()

const loaders = {
  vuln: () => import('./VulnScanPage.vue'),
  license: () => import('./LicenseScanPage.vue'),
  secret: () => import('./SecretScanPage.vue'),
  ai: () => import('./AIAnalysisPage.vue'),
}
const pages = Object.fromEntries(
  Object.entries(loaders).map(([type, loader]) => [type, defineAsyncComponent({ loader, loadingComponent: PageLoadingFallback, delay: 200 })]),
)

const page = computed(() => {
  const type = typeof route.query.type === 'string' ? route.query.type : 'vuln'
  return pages[type] ?? pages.vuln
})
</script>
