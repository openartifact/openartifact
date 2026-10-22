<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<script setup lang="ts">
import { t } from '../i18n/en'
</script>

<template>
  <div class="flex flex-col items-center justify-center min-h-[200px] gap-3 text-center p-6">
    <p class="text-sm text-danger font-medium">{{ t('common.pageLoadFailed') }}</p>
    <button
      class="text-xs text-primary hover:underline"
      @click="$router.go(0)"
    >
      {{ t('graph.retry') }}
    </button>
  </div>
</template>
