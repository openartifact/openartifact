<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex flex-col items-center pt-8">
    <div class="w-12 h-12 rounded-full bg-warning-soft flex items-center justify-center">
      <ExclamationTriangleIcon class="w-6 h-6 text-warning" aria-hidden="true" />
    </div>
    <EmptyState
      :title="t('shell.notFoundTitle')"
      :description="t('shell.notFoundBody')"
      :action-label="t('shell.backToDashboard')"
      action-to="/"
    />
  </div>
</template>

<script setup lang="ts">
import { ExclamationTriangleIcon } from '@heroicons/vue/24/outline'
import EmptyState from '../components/EmptyState.vue'
import { t } from '../i18n'
</script>
