<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="open"
    :eyebrow="t('sec.vex.eyebrow')"
    :title="`${t('sec.vex.decisionFor')} ${vulnId}`"
    :description="`${repo} · ${artifactLabel ?? artifactId}`"
    max-width="max-w-lg"
    @close="close"
  >
    <form class="space-y-4" data-testid="vex-form" novalidate @submit.prevent="save">
      <div v-if="existing" class="flex items-center gap-2 text-xs text-muted" data-testid="vex-current">
        <VexStatusBadge :status="existing.status" />
        <span v-if="existing.updated_at">{{ formatDateTime(existing.updated_at) }}</span>
        <span v-if="existing.created_by">· {{ existing.created_by }}</span>
      </div>

      <FormField id="vex-status" :label="t('sec.vex.field.status')">
        <select id="vex-status" v-model="form.status" :class="inputClass" data-testid="vex-status">
          <option v-for="s in VEX_STATUSES" :key="s" :value="s">{{ vexStatusLabel(s) }}</option>
        </select>
      </FormField>

      <FormField
        v-if="form.status === 'not_affected'"
        id="vex-justification"
        :label="t('sec.vex.field.justification')"
        :hint="t('sec.vex.field.justificationHint')"
        :error="errorText('justification')"
      >
        <select
          id="vex-justification"
          v-model="form.justification"
          required
          aria-required="true"
          :aria-invalid="errorText('justification') ? 'true' : undefined"
          :aria-describedby="errorText('justification') ? 'vex-justification-error' : undefined"
          :class="inputClass"
          data-testid="vex-justification"
        >
          <option value="" disabled>{{ t('sec.vex.field.justification') }}</option>
          <option v-for="j in VEX_JUSTIFICATIONS" :key="j" :value="j">{{ vexJustificationLabel(j) }}</option>
        </select>
      </FormField>

      <FormField id="vex-impact" :label="t('sec.vex.field.impact')" :hint="t('sec.vex.field.impactHint')" :error="errorText('impact')">
        <textarea
          id="vex-impact"
          v-model="form.impact"
          rows="3"
          :maxlength="VEX_STATEMENT_MAX_LENGTH"
          :class="textareaClass"
          data-testid="vex-impact"
        />
      </FormField>

      <FormField id="vex-action" :label="t('sec.vex.field.action')" :hint="t('sec.vex.field.actionHint')" :error="errorText('action')">
        <textarea
          id="vex-action"
          v-model="form.action"
          rows="3"
          :maxlength="VEX_STATEMENT_MAX_LENGTH"
          :class="textareaClass"
          data-testid="vex-action"
        />
      </FormField>
    </form>

    <!-- Nested so clicks inside it are not "outside" this dialog. -->
    <ConfirmDialog
      :open="confirmOpen"
      :title="t('sec.vex.deleteTitle')"
      :description="`${vulnId} · ${artifactLabel ?? artifactId}`"
      :confirm-label="t('sec.vex.delete')"
      :busy="deleting"
      @confirm="remove"
      @close="confirmOpen = false"
    >
      {{ t('sec.vex.deleteDescription') }}
    </ConfirmDialog>

    <template #footer>
      <AppButton
        v-if="existing"
        variant="danger"
        size="md"
        class="mr-auto"
        :disabled="busy"
        data-testid="vex-delete"
        @click="confirmOpen = true"
      >
        {{ t('sec.vex.deleteDecision') }}
      </AppButton>
      <AppButton variant="secondary" size="md" :disabled="busy" @click="close">
        {{ t('common.cancel') }}
      </AppButton>
      <AppButton variant="primary" size="md" :loading="saving" :disabled="busy" data-testid="vex-save" @click="save">
        {{ saving ? t('sec.vex.saving') : t('sec.vex.save') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import FormField from './FormField.vue'
import VexStatusBadge from './VexStatusBadge.vue'
import {
  deleteVexStatement,
  putVexStatement,
  VEX_JUSTIFICATIONS,
  VEX_STATEMENT_MAX_LENGTH,
  VEX_STATUSES,
  type VexRecord,
} from '../api/vex'
import { formatDateTime } from '../lib/datetime'
import {
  validateVexForm,
  vexDecisionBody,
  vexFormState,
  vexJustificationLabel,
  vexStatusLabel,
  type VexFormErrors,
} from '../lib/vex'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{
  open: boolean
  repo: string
  /** The scan's `artifact_id`: the decision is recorded for this artifact. */
  artifactId: string
  /** Human label of the artifact (`name@version`); the id when absent. */
  artifactLabel?: string
  vulnId: string
  /** The decision already recorded for the finding, prefilled; null for a new one. */
  existing: VexRecord | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', record: VexRecord): void
  (e: 'deleted', id: string): void
}>()

const toasts = useToastStore()

const inputClass =
  'w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50'
const textareaClass =
  'w-full px-3 py-2 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50 resize-y'

const form = ref(vexFormState(props.existing))
// Errors are shown once a save was attempted (or while the form stays invalid after that).
const attempted = ref(false)
const saving = ref(false)
const deleting = ref(false)
const confirmOpen = ref(false)
const busy = computed(() => saving.value || deleting.value)

const errors = computed<VexFormErrors>(() => validateVexForm(form.value))

function errorText(field: keyof VexFormErrors): string | undefined {
  const code = attempted.value ? errors.value[field] : undefined
  return code ? t(`sec.vex.error.${code}`) : undefined
}

// Every time the dialog opens (or another decision is edited) it starts from the recorded decision.
watch(
  () => [props.open, props.vulnId, props.artifactId, props.existing?.id] as const,
  () => {
    if (!props.open) return
    form.value = vexFormState(props.existing)
    attempted.value = false
    confirmOpen.value = false
  },
)

function close(): void {
  if (!busy.value) emit('close')
}

async function save(): Promise<void> {
  if (busy.value) return
  attempted.value = true
  if (Object.keys(errors.value).length > 0) return
  saving.value = true
  try {
    const record = await putVexStatement(props.repo, vexDecisionBody(props.artifactId, props.vulnId, form.value))
    toasts.success(t('sec.vex.saved'))
    emit('saved', record)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vex.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function remove(): Promise<void> {
  const id = props.existing?.id
  if (!id || busy.value) return
  deleting.value = true
  try {
    await deleteVexStatement(props.repo, id)
    toasts.success(t('sec.vex.deleted'))
    confirmOpen.value = false
    emit('deleted', id)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vex.deleteFailed'))
  } finally {
    deleting.value = false
  }
}
</script>
