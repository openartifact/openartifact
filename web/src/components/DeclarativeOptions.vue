<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The scope of a diff/apply: which kinds are looked at, and whether objects the manifest no longer lists are deleted. -->
<template>
  <div class="space-y-4" data-testid="declarative-options">
    <fieldset>
      <legend class="text-sm font-medium text-foreground">{{ t('sx.declarative.kinds') }}</legend>
      <p class="text-xs text-muted">{{ t('sx.declarative.kinds.hint') }}</p>
      <div class="mt-2 flex flex-wrap gap-x-4 gap-y-2">
        <label v-for="k in DECLARATIVE_KINDS" :key="k" class="flex items-center gap-1.5 text-sm text-foreground max-md:min-h-11">
          <input v-model="kinds" type="checkbox" :value="k" class="h-4 w-4 rounded border-border-strong" />
          {{ k }}
        </label>
      </div>
    </fieldset>

    <div class="grid gap-3 md:grid-cols-2">
      <label v-for="o in flags" :key="o.id" class="flex items-start gap-2 text-sm max-md:min-h-11">
        <input v-model="o.model.value" type="checkbox" class="mt-0.5 h-4 w-4 rounded border-border-strong" :data-testid="`opt-${o.id}`" />
        <span>
          <span class="block font-medium text-foreground">{{ o.label }}</span>
          <span class="block text-xs text-muted">{{ o.hint }}</span>
        </span>
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { DECLARATIVE_KINDS } from '../api/declarative'
import { t } from '../i18n'

const kinds = defineModel<string[]>('kinds', { required: true })
const prune = defineModel<boolean>('prune', { required: true })
const pruneAll = defineModel<boolean>('pruneAll', { required: true })
const forceSecrets = defineModel<boolean>('forceSecrets', { required: true })
const continueOnError = defineModel<boolean>('continueOnError', { required: true })

const flags = computed(() => [
  { id: 'prune', model: prune, label: t('sx.declarative.prune'), hint: t('sx.declarative.prune.hint') },
  { id: 'prune-all', model: pruneAll, label: t('sx.declarative.pruneAll'), hint: t('sx.declarative.pruneAll.hint') },
  { id: 'force-secrets', model: forceSecrets, label: t('sx.declarative.forceSecrets'), hint: t('sx.declarative.forceSecrets.hint') },
  { id: 'continue', model: continueOnError, label: t('sx.declarative.continue'), hint: t('sx.declarative.continue.hint') },
])
</script>
