<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Add / edit drawer of one curation rule: the action, the package and version patterns and a reason. -->
<template>
  <Drawer :open="open" :title="editing ? t('curation.editTitle') : t('curation.addTitle')" :eyebrow="t('curation.title')" :busy="saving" @close="emit('close')">
    <form id="curation-rule-form" class="space-y-4" novalidate @submit.prevent="emit('save')">
      <div role="radiogroup" :aria-label="t('curation.field.action')" class="grid gap-2 sm:grid-cols-2">
        <button
          v-for="action in CURATION_ACTIONS"
          :key="action"
          type="button"
          role="radio"
          :aria-checked="form.action === action"
          :data-testid="`rule-action-${action}`"
          class="min-h-11 rounded-lg border px-3 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          :class="form.action === action ? 'border-primary bg-primary-soft' : 'border-border-strong bg-surface hover:bg-surface-hover'"
          @click="form.action = action"
        >
          <span class="block text-sm font-medium text-foreground">{{ actionText(action).label }}</span>
          <span class="block text-xs text-muted">{{ actionText(action).hint }}</span>
        </button>
      </div>

      <FormField :label="t('curation.field.package')" id="curation-package" :hint="t('curation.field.packageHint')">
        <input
          id="curation-package"
          v-model="form.packagePattern"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :placeholder="t('curation.field.packagePlaceholder')"
          :class="MONO_INPUT_CLASS"
        />
      </FormField>

      <FormField :label="t('curation.field.version')" id="curation-version" :hint="t('curation.field.versionHint')">
        <input
          id="curation-version"
          v-model="form.versionPattern"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :placeholder="t('curation.field.versionPlaceholder')"
          :class="MONO_INPUT_CLASS"
        />
      </FormField>

      <FormField :label="t('curation.field.reason')" id="curation-reason" :hint="t('curation.field.reasonHint')">
        <input id="curation-reason" v-model="form.reason" type="text" autocomplete="off" :class="INPUT_CLASS" />
      </FormField>

      <p class="rounded-lg bg-surface-hover px-3 py-2 text-xs text-muted" data-testid="glob-help">{{ t('curation.globHelp') }}</p>
      <p v-if="error" role="alert" class="text-sm text-danger" data-testid="rule-error">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="curation-rule-form" :loading="saving" :disabled="!valid" data-testid="rule-save">
        {{ editing ? t('common.save') : t('curation.addAction') }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import { actionText, CURATION_ACTIONS, type RuleForm } from '../../lib/curation'
import { INPUT_CLASS, MONO_INPUT_CLASS } from '../../lib/formClasses'
import { t } from '../../i18n'

defineProps<{
  open: boolean
  /** Editing an existing rule, not adding one. */
  editing: boolean
  saving: boolean
  valid: boolean
  /** The server's reason for refusing the last save. */
  error: string
}>()

const form = defineModel<RuleForm>({ required: true })

const emit = defineEmits<{ close: []; save: [] }>()
</script>
