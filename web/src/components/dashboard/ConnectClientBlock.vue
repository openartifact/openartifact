<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <DashSection
    :title="t('dashboard.connect.title')"
    heading-id="connect"
    :state="state"
    :empty="repos.length === 0"
    @retry="emit('retry')"
  >
    <template #empty>
      <StateBlock variant="empty" :title="t('dashboard.connect.empty')" :hint="t('dashboard.connect.emptyHint')">
        <template v-if="canCreate" #action>
          <AppButton to="/repos?new=1">{{ t('repos.new') }}</AppButton>
        </template>
      </StateBlock>
    </template>
    <div class="space-y-3">
      <div class="flex flex-wrap items-center gap-2">
        <label for="connect-repo" class="text-sm text-muted">{{ t('dashboard.connect.repo') }}</label>
        <select
          id="connect-repo"
          v-model="selected"
          class="h-9 min-w-0 max-w-full rounded-lg border border-border-strong bg-surface px-3 text-sm text-foreground focus:ring-2 focus:ring-primary max-md:h-11"
        >
          <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }} ({{ formatLabel(r.format) }})</option>
        </select>
        <FormatBadge v-if="current" :format="current.format" size="sm" />
      </div>
      <ConnectClientPanel
        v-if="current"
        :format="current.format"
        :repo="current.name"
        :read-only="current.repo_type === 'proxy' || current.repo_type === 'group'"
        titled
      />
      <p class="text-xs text-muted">
        {{ t('dashboard.connect.tokenHint') }}
        <router-link to="/settings/tokens?new=1" class="text-primary hover:underline">{{ t('dashboard.connect.createToken') }}</router-link>
      </p>
    </div>
  </DashSection>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { RepositoryWithStats } from '../../api/dashboard'
import AppButton from '../AppButton.vue'
import ConnectClientPanel from '../ConnectClientPanel.vue'
import FormatBadge from '../FormatBadge.vue'
import StateBlock from '../ui/StateBlock.vue'
import DashSection from './DashSection.vue'
import { t } from '../../i18n/en'
import { formatLabel } from '../../lib/formats'

const props = defineProps<{ repos: RepositoryWithStats[]; state: 'loading' | 'ready' | 'error'; canCreate: boolean }>()
const emit = defineEmits<{ retry: [] }>()

const selected = ref('')
const current = computed(() => props.repos.find((r) => r.name === selected.value))
// Start on the most recently updated repository (the list arrives sorted that way).
watch(
  () => props.repos,
  (repos) => {
    if (!repos.some((r) => r.name === selected.value)) selected.value = repos[0]?.name ?? ''
  },
  { immediate: true },
)
</script>
