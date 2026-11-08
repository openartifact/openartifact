<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <fieldset role="radiogroup" :aria-describedby="hintId">
    <legend class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.icon') }}</legend>
    <div class="grid grid-cols-2 gap-2 sm:grid-cols-3">
      <label v-for="option in options" :key="option.value" class="relative" :class="option.value === '' ? 'col-span-full' : ''">
        <input
          v-model="model"
          type="radio"
          name="sso-icon"
          :value="option.value"
          class="peer sr-only"
        />
        <span
          class="flex min-h-9 cursor-pointer items-center gap-2 rounded-lg border border-border-strong bg-bg px-2.5 py-2 text-sm text-foreground transition-colors hover:bg-surface-hover peer-checked:border-primary peer-checked:ring-1 peer-checked:ring-primary peer-focus-visible:ring-2 peer-focus-visible:ring-primary"
        >
          <SsoIcon v-if="option.icon" :icon="option.icon" class="shrink-0" />
          <span class="min-w-0 truncate">{{ option.label }}</span>
        </span>
      </label>
    </div>
    <p :id="hintId" class="text-xs text-muted mt-1">{{ t('adm.sso.field.iconHint') }}</p>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, useId } from 'vue'
import SsoIcon from '../../../components/SsoIcon.vue'
import { t, tp } from '../../../i18n'
import { SSO_ICON_KEYS, ssoIconLabel } from '../../../lib/brandIcons'

// The stored icon key; "" is automatic (the server derives it from the provider settings).
const model = defineModel<string>({ required: true })
// What automatic resolves to for the saved provider; absent for a new one.
const props = defineProps<{ resolved?: string }>()

const hintId = useId()

const options = computed(() => [
  {
    value: '',
    icon: props.resolved ?? '',
    label: props.resolved
      ? tp('adm.sso.icon.automaticResolved', { vendor: ssoIconLabel(props.resolved) })
      : t('adm.sso.icon.automatic'),
  },
  ...SSO_ICON_KEYS.map((key) => ({ value: key as string, icon: key as string, label: ssoIconLabel(key) })),
])
</script>
