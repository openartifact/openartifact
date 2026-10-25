<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Stand-in for a settings section whose page is not built yet (the header is the section page's). -->
<template>
  <div class="rounded-lg border border-border bg-surface">
    <StateBlock variant="off" :title="t('ui.settings.placeholder.title')" :hint="t('ui.settings.placeholder.hint')" />
  </div>
</template>

<script setup lang="ts">
import StateBlock from '../../components/ui/StateBlock.vue'
import { t } from '../../i18n/en'
</script>
