<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Stand-in for the settings hub; the settings slice replaces it. -->
<template>
  <div>
    <PageHeader :title="t('ui.settings.hub')" />
    <section v-for="g in groups" :key="g.group.id" class="mb-6">
      <h2 class="mb-2 text-sm font-semibold text-foreground">{{ t(g.group.titleKey) }}</h2>
      <ul class="divide-y divide-border rounded-lg border border-border bg-surface">
        <li v-for="s in g.sections" :key="s.id">
          <router-link
            :to="`/settings/${s.id}`"
            class="flex min-h-11 flex-col px-4 py-2.5 hover:bg-surface-hover"
          >
            <span class="text-sm font-medium text-foreground">{{ t(s.titleKey) }}</span>
            <span class="text-xs text-muted">{{ t(s.descKey) }}</span>
          </router-link>
        </li>
      </ul>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import { visibleSettingsGroups } from '../../lib/settingsSections'
import { useSessionStore } from '../../stores/session'
import { t } from '../../i18n/en'

const session = useSessionStore()
const groups = computed(() => visibleSettingsGroups(session.isAdmin))
</script>
