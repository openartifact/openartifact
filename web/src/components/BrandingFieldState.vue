<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- "Overridden / Default" marker of a branding setting, with a reset button while overridden. -->
<template>
  <span class="inline-flex items-center gap-2">
    <span
      class="rounded-full px-2 py-0.5 text-caption font-medium"
      :class="overridden ? 'bg-primary-soft text-primary' : 'bg-surface-hover text-muted'"
      data-testid="field-state"
    >{{ overridden ? t('adm.branding.state.overridden') : t('adm.branding.state.default') }}</span>
    <button
      v-if="overridden"
      type="button"
      class="text-caption text-muted underline underline-offset-2 hover:text-foreground disabled:opacity-50"
      :disabled="busy"
      data-testid="field-reset"
      @click="emit('reset')"
    >{{ t('adm.branding.resetField') }}</button>
  </span>
</template>

<script setup lang="ts">
import { t } from '../i18n/en'

withDefaults(defineProps<{ overridden: boolean; busy?: boolean }>(), { busy: false })
const emit = defineEmits<{ (e: 'reset'): void }>()
</script>
