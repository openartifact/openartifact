<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Stand-in for a settings section whose page is not built yet. -->
<template>
  <div>
    <PageHeader :title="settingsSectionTitle(route.params.section)" />
    <div class="rounded-lg border border-border bg-surface">
      <StateBlock variant="off" :title="t('ui.settings.placeholder.title')" :hint="t('ui.settings.placeholder.hint')" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { useRoute } from 'vue-router'
import PageHeader from '../../components/PageHeader.vue'
import StateBlock from '../../components/ui/StateBlock.vue'
import { settingsSectionTitle } from '../../lib/settingsSections'
import { t } from '../../i18n/en'

const route = useRoute()
</script>
