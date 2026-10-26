<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('tiering.dryRun.title')">
    <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
      <div class="min-w-0 mr-auto">
        <h2 class="text-sm font-semibold text-foreground">{{ t('tiering.dryRun.title') }}</h2>
        <p class="text-caption text-muted">{{ t('tiering.dryRun.description') }}</p>
      </div>
      <AppButton variant="secondary" size="sm" :loading="dry.loading.value" data-testid="simulate-all" @click="dry.run()">
        <BeakerIcon v-if="!dry.loading.value" class="w-4 h-4" aria-hidden="true" />
        {{ t('tiering.dryRun.simulateAll') }}
      </AppButton>
    </div>
    <div v-if="dry.error.value || dry.result.value" class="px-5 py-4" aria-live="polite">
      <p v-if="dry.error.value" class="text-sm text-danger" role="alert">{{ dry.error.value }}</p>
      <TierDryRunResult v-else-if="dry.result.value" :result="dry.result.value" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { BeakerIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import TierDryRunResult from './TierDryRunResult.vue'
import { useTierDryRun } from '../composables/useTierDryRun'
import { t } from '../i18n'

const dry = useTierDryRun()
</script>
