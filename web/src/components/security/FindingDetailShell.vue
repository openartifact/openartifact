<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The loading and error states every finding detail shares; the loaded detail goes in the default slot. -->
<template>
  <div>
    <div v-if="loading" class="flex justify-center py-8 text-muted" role="status">
      <ArrowPathIcon class="h-5 w-5 animate-spin" aria-hidden="true" />
      <span class="sr-only">{{ t('security.detail.loading') }}</span>
    </div>
    <p v-else-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <slot v-else />
  </div>
</template>

<script setup lang="ts">
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import { t } from '../../i18n'

defineProps<{ loading: boolean; error: string }>()
</script>
