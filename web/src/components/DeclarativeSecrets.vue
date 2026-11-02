<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The `secretRef` values a plan sends: one password field per environment reference the manifest names. -->
<template>
  <section class="rounded-lg border border-border bg-surface p-4" data-testid="declarative-secrets">
    <h2 class="text-sm font-semibold text-foreground">{{ t('sx.declarative.secrets') }}</h2>
    <p class="mt-0.5 text-xs text-muted">{{ t('sx.declarative.secrets.hint') }}</p>
    <div class="mt-3 grid gap-3 md:grid-cols-2">
      <template v-for="s in secrets" :key="s.env ?? s.file">
        <FormField v-if="s.env" :id="`secret-${s.env}`" :label="s.env" :hint="s.provided ? t('sx.declarative.secrets.provided') : undefined">
          <input
            :id="`secret-${s.env}`"
            v-model="values[s.env]"
            type="password"
            autocomplete="off"
            :class="MONO_INPUT_CLASS"
            :placeholder="t('sx.declarative.secrets.placeholder')"
          />
        </FormField>
        <p v-else class="text-sm text-danger" role="alert">{{ tp('sx.declarative.secrets.file', { path: s.file ?? '' }) }}</p>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import FormField from './FormField.vue'
import type { DeclarativeRunSecret } from '../api/declarative'
import { MONO_INPUT_CLASS } from '../lib/formClasses'
import { t, tp } from '../i18n'

defineProps<{ secrets: DeclarativeRunSecret[] }>()
const values = defineModel<Record<string, string>>({ required: true })
</script>
