<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The outcome of a connection test: the server's version and permissions, or why it failed. -->
<template>
  <div v-if="result" class="rounded-xl border border-success/30 bg-success-soft p-4 space-y-2" role="status" data-testid="dt-test-result">
    <p class="text-sm font-semibold text-success">{{ t('adm.dt.testOk') }}</p>
    <p class="text-xs text-foreground">
      {{ t('adm.dt.testVersion') }}: <span class="font-mono">{{ result.version }}</span>
    </p>
    <div class="text-xs text-foreground">
      <p class="mb-1">{{ t('adm.dt.testPermissions') }}:</p>
      <div v-if="permissions.length > 0" class="flex flex-wrap gap-1.5">
        <span v-for="p in permissions" :key="p" class="px-2 py-0.5 rounded-md bg-success/15 text-success text-caption font-mono">{{ p }}</span>
      </div>
      <span v-else class="text-muted">{{ t('adm.dt.testNoPermissions') }}</span>
    </div>
    <div v-if="missing.length > 0" class="rounded-lg border border-warning/30 bg-warning-soft p-3 text-xs" role="alert" data-testid="dt-test-missing">
      <p class="font-semibold text-warning">{{ t('adm.dt.testMissingWarn') }}</p>
      <p class="mt-1 text-foreground">{{ t('adm.dt.testMissing') }}:</p>
      <div class="flex flex-wrap gap-1.5 mt-1">
        <span v-for="p in missing" :key="p" class="px-2 py-0.5 rounded-md bg-warning/15 text-warning text-caption font-mono">{{ p }}</span>
      </div>
    </div>
  </div>
  <div v-if="error" class="rounded-xl border border-danger/30 bg-danger-soft p-4 text-sm text-danger" role="alert">
    <p class="font-semibold">{{ t('adm.dt.testFailed') }}</p>
    <p class="text-xs mt-0.5 font-mono break-all">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { DependencyTrackTest } from '../../../api/dependencyTrack'
import { t } from '../../../i18n'

const props = defineProps<{ result: DependencyTrackTest | null; error: string }>()
const permissions = computed(() => props.result?.permissions ?? [])
const missing = computed(() => props.result?.missing_permissions ?? [])
</script>
