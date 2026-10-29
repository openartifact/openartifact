<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The connection fields of a Dependency-Track configuration: server URL, API
  key (write-only, with its stored state and a remove button), and the project
  name and version templates. Children of the parent's two-column grid; the
  global settings page (`global`) adds the explanatory hints.
-->
<template>
  <div class="col-span-2 sm:col-span-1">
    <label :for="ids.baseUrl" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.baseUrl') }}</label>
    <input
      :id="ids.baseUrl"
      v-model="form.base_url"
      type="text"
      :placeholder="global ? 'https://dtrack.example.com' : undefined"
      :class="MONO_INPUT_CLASS"
    />
    <p v-if="global" class="text-caption text-muted mt-1">{{ t('adm.dt.baseUrlHint') }}</p>
  </div>

  <div class="col-span-2 sm:col-span-1">
    <label :for="ids.apiKey" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.apiKey') }}</label>
    <input
      :id="ids.apiKey"
      v-model="apiKey"
      type="password"
      autocomplete="new-password"
      :placeholder="t('adm.dt.apiKeyPlaceholder')"
      :class="MONO_INPUT_CLASS"
    />
    <div class="flex items-center gap-3 mt-1">
      <span class="text-caption" :class="apiKeySet ? 'text-success' : 'text-muted'" :data-testid="global ? 'dt-key-state' : undefined">
        {{ apiKeySet ? t('adm.dt.apiKeyStored') : t('adm.dt.apiKeyNone') }}
      </span>
      <button
        v-if="apiKeySet"
        type="button"
        class="text-caption font-medium text-muted hover:text-danger transition-colors"
        :disabled="saving"
        @click="emit('removeKey')"
      >
        {{ t('adm.dt.removeKey') }}
      </button>
    </div>
    <p v-if="global" class="text-caption text-muted mt-1">{{ t('adm.dt.apiKeyHint') }}</p>
  </div>

  <div class="col-span-2 sm:col-span-1">
    <label :for="ids.projectName" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.projectName') }}</label>
    <input :id="ids.projectName" v-model="form.project_name" type="text" :placeholder="t('adm.dt.projectNamePlaceholder')" :class="MONO_INPUT_CLASS" />
  </div>
  <div class="col-span-2 sm:col-span-1">
    <label :for="ids.projectVersion" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.projectVersion') }}</label>
    <input :id="ids.projectVersion" v-model="form.project_version" type="text" :placeholder="t('adm.dt.projectVersionPlaceholder')" :class="MONO_INPUT_CLASS" />
  </div>
  <p class="col-span-2 text-caption text-muted -mt-2">{{ t('adm.dt.templateHint') }}</p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { MONO_INPUT_CLASS } from '../../../lib/formClasses'
import { t } from '../../../i18n'

const props = defineProps<{
  /** The global settings page, not a repository override. */
  global?: boolean
  apiKeySet: boolean
  saving: boolean
}>()

const form = defineModel<{ base_url: string; project_name: string; project_version: string }>('form', { required: true })
const apiKey = defineModel<string>('apiKey', { required: true })

const emit = defineEmits<{ removeKey: [] }>()

const ids = computed(() =>
  props.global
    ? { baseUrl: 'dt-base-url', apiKey: 'dt-api-key', projectName: 'dt-project-name', projectVersion: 'dt-project-version' }
    : { baseUrl: 'dt-ovr-url', apiKey: 'dt-ovr-key', projectName: 'dt-ovr-name', projectVersion: 'dt-ovr-version' },
)
</script>
