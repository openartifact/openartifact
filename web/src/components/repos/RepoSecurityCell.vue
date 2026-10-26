<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Security counts of one repository row: every figure with its word, never colour alone. -->
<template>
  <span v-if="state.kind === 'unknown' || state.kind === 'empty'" class="text-xs text-muted">—</span>
  <span v-else-if="state.kind === 'unscanned'" class="text-xs text-muted">{{ t('rl.security.unscanned') }}</span>
  <span v-else-if="state.kind === 'clean'" class="inline-flex items-center gap-1.5 text-xs font-medium text-success">
    <CheckCircleIcon class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
    {{ t('rl.security.clean') }}
  </span>
  <span v-else class="flex flex-wrap items-center gap-x-3 gap-y-0.5" data-testid="security-findings">
    <SeverityBadge v-if="state.security.critical" severity="critical" :count="state.security.critical" />
    <SeverityBadge v-if="state.security.high" severity="high" :count="state.security.high" />
    <SeverityBadge v-if="state.security.medium" severity="medium" :count="state.security.medium" />
    <SeverityBadge v-if="state.security.low" severity="low" :count="state.security.low" />
    <span v-if="state.security.secrets" class="text-xs font-medium text-danger">{{ tp('rl.security.secrets', { n: String(state.security.secrets) }) }}</span>
    <span v-if="state.security.license_violations" class="text-xs font-medium text-warning">{{ tp('rl.security.licenses', { n: String(state.security.license_violations) }) }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircleIcon } from '@heroicons/vue/20/solid'
import SeverityBadge from '../ui/SeverityBadge.vue'
import { securityState } from '../../lib/repoList'
import type { RepositoryListItem } from '../../api/repositories'
import { t, tp } from '../../i18n/en'

const props = defineProps<{ item: Pick<RepositoryListItem, 'stats'> }>()
const state = computed(() => securityState(props.item))
</script>
