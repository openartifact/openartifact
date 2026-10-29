<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- A settings section whose only header action is "Refresh". -->
<template>
  <SectionActions>
    <AppButton variant="secondary" size="sm" class="max-md:h-11" :loading="loading" @click="emit('refresh')">{{ t('sx.refresh') }}</AppButton>
  </SectionActions>
</template>

<script setup lang="ts">
import AppButton from './AppButton.vue'
import SectionActions from './SectionActions.vue'
import { t } from '../i18n'

defineProps<{ loading: boolean }>()
const emit = defineEmits<{ refresh: [] }>()
</script>
