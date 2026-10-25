<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- `/settings/:section`: resolves the id through the registry and renders that section's page. -->
<template>
  <div v-if="!section">
    <PageHeader :title="t('ui.settings.unknown')" />
    <div class="rounded-lg border border-border bg-surface">
      <StateBlock variant="empty" :title="t('ui.settings.unknown')" :hint="t('ui.settings.unknownHint')" />
    </div>
  </div>
  <component :is="page" v-else :key="section.id" />
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { useRoute } from 'vue-router'
import PageHeader from '../../components/PageHeader.vue'
import StateBlock from '../../components/ui/StateBlock.vue'
import PageLoadingFallback from '../../components/PageLoadingFallback.vue'
import { findSettingsSection } from '../../lib/settingsSections'
import { useSessionStore } from '../../stores/session'
import { t } from '../../i18n/en'

const route = useRoute()
const session = useSessionStore()
const section = computed(() => findSettingsSection(route.params.section))

const page = computed(() => {
  const s = section.value
  if (!s) return null
  const loader = session.isAdmin && s.loadAdmin ? s.loadAdmin : s.load
  return defineAsyncComponent({ loader, loadingComponent: PageLoadingFallback, delay: 200 })
})
</script>
