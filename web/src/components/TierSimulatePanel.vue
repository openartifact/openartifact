<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <CardSection :title="t('tiering.dryRun.title')" :description="t('tiering.dryRun.description')">
    <template #action>
      <AppButton variant="secondary" size="sm" :loading="dry.loading.value" data-testid="simulate-all" @click="dry.run()">
        <BeakerIcon v-if="!dry.loading.value" class="w-4 h-4" aria-hidden="true" />
        {{ t('tiering.dryRun.simulateAll') }}
      </AppButton>
    </template>
    <div v-if="dry.error.value || dry.result.value" class="px-5 py-4" aria-live="polite">
      <p v-if="dry.error.value" class="text-sm text-danger" role="alert">{{ dry.error.value }}</p>
      <TierDryRunResult v-else-if="dry.result.value" :result="dry.result.value" />
    </div>
  </CardSection>
</template>

<script setup lang="ts">
import CardSection from './ui/CardSection.vue'
import { BeakerIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import TierDryRunResult from './TierDryRunResult.vue'
import { useTierDryRun } from '../composables/useTierDryRun'
import { t } from '../i18n'

const dry = useTierDryRun()
</script>
