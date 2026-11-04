<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="rounded-lg border border-border p-4 space-y-1.5" data-testid="dt-effective">
    <div class="flex items-center gap-2">
      <h4 class="text-xs font-semibold text-foreground">{{ t('adm.dt.effective') }}</h4>
      <StatusBadge :label="t(`adm.dt.source.${effective.source ?? 'none'}`)" variant="neutral" />
      <StatusBadge :label="effective.ready ? t('adm.dt.ready') : t('adm.dt.notReady')" :variant="effective.ready ? 'success' : 'warning'" />
    </div>
    <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
      <dt class="text-muted">{{ t('adm.dt.baseUrl') }}</dt>
      <dd class="font-mono text-foreground break-all">{{ effective.base_url || '—' }}</dd>
      <dt class="text-muted">{{ t('adm.dt.apiKey') }}</dt>
      <dd class="text-foreground">{{ effective.api_key_set ? t('adm.dt.apiKeyStored') : t('adm.dt.apiKeyNone') }}</dd>
      <dt class="text-muted">{{ t('adm.dt.projectName') }}</dt>
      <dd class="font-mono text-foreground">{{ effective.project_name || '—' }}</dd>
      <dt class="text-muted">{{ t('adm.dt.projectVersion') }}</dt>
      <dd class="font-mono text-foreground">{{ effective.project_version || '—' }}</dd>
      <dt class="text-muted">{{ t('adm.dt.autoPush') }}</dt>
      <dd class="text-foreground">{{ effective.auto_push ? t('adm.dt.on') : t('adm.dt.off') }}</dd>
      <dt class="text-muted">{{ t('adm.dt.pushVex') }}</dt>
      <dd class="text-foreground">{{ effective.push_vex ? t('adm.dt.on') : t('adm.dt.off') }}</dd>
    </dl>
  </div>
</template>

<script setup lang="ts">
import StatusBadge from '../../../components/StatusBadge.vue'
import type { DependencyTrackRepository } from '../../../api/dependencyTrack'
import { t } from '../../../i18n'

defineProps<{ effective: NonNullable<DependencyTrackRepository['effective']> }>()
</script>
