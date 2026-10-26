<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="open"
    :eyebrow="t('tiering.policy.eyebrow')"
    :title="policy ? t('tiering.policy.editTitle') : t('tiering.policy.addTitle')"
    max-width="max-w-3xl"
    @close="emit('close')"
  >
    <form id="tier-policy-form" class="space-y-5" @submit.prevent="save">
      <div class="grid grid-cols-1 sm:grid-cols-[1fr_8rem] gap-3">
        <div>
          <label for="tp-name" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('tiering.policy.field.name') }} <span class="text-danger">*</span>
          </label>
          <input id="tp-name" v-model="form.name" type="text" autocomplete="off" :class="inputClass" />
        </div>
        <div>
          <label for="tp-position" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('tiering.policy.field.position') }}
          </label>
          <input id="tp-position" v-model.number="form.position" type="number" min="0" step="1" :class="inputClass" />
        </div>
      </div>
      <p class="text-caption text-muted -mt-3">{{ t('tiering.policy.field.positionHint') }}</p>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label for="tp-from" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('tiering.policy.field.from') }}</label>
          <select id="tp-from" v-model="form.fromTier" :class="inputClass">
            <option value="">{{ t('tiering.policy.anyTier') }}</option>
            <option v-for="tier in tierNames" :key="tier" :value="tier">{{ tier }}</option>
          </select>
        </div>
        <div>
          <label for="tp-target" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('tiering.policy.field.target') }} <span class="text-danger">*</span>
          </label>
          <select id="tp-target" v-model="form.targetTier" :class="inputClass">
            <option v-for="tier in tierNames" :key="tier" :value="tier">{{ tier }}</option>
          </select>
        </div>
      </div>

      <fieldset class="space-y-3">
        <legend class="text-xs font-semibold text-foreground">{{ t('tiering.policy.conditions') }}</legend>
        <p class="text-caption text-muted">{{ t('tiering.policy.conditionsHint') }}</p>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="tp-age" class="block text-xs text-muted mb-1">{{ t('tiering.policy.field.age') }}</label>
            <input id="tp-age" v-model.number="form.ageDays" type="number" min="0" step="any" :class="inputClass" />
          </div>
          <div>
            <label for="tp-idle" class="block text-xs text-muted mb-1">{{ t('tiering.policy.field.idle') }}</label>
            <input id="tp-idle" v-model.number="form.idleDays" type="number" min="0" step="any" :class="inputClass" />
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div v-for="f in sizeFields" :key="f.id">
            <label :for="f.id" class="block text-xs text-muted mb-1">{{ f.label }}</label>
            <div class="flex gap-2">
              <input :id="f.id" v-model.number="f.model.value" type="number" min="0" step="any" :class="inputClass" />
              <select v-model="f.model.unit" :aria-label="`${f.label} (${t('tiering.policy.unit')})`" class="shrink-0 w-20 h-9 px-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary">
                <option v-for="u in TIER_SIZE_UNITS" :key="u.label" :value="u.label">{{ u.label }}</option>
              </select>
            </div>
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="tp-repo" class="block text-xs text-muted mb-1">{{ t('tiering.policy.field.repoGlob') }}</label>
            <input id="tp-repo" v-model="form.repoGlob" type="text" autocomplete="off" placeholder="team-*" :class="[inputClass, 'font-mono']" />
          </div>
          <div>
            <label for="tp-path" class="block text-xs text-muted mb-1">{{ t('tiering.policy.field.pathGlob') }}</label>
            <input id="tp-path" v-model="form.pathGlob" type="text" autocomplete="off" placeholder="**/*.jar" :class="[inputClass, 'font-mono']" />
          </div>
        </div>
        <p class="text-caption text-muted">{{ t('tiering.policy.globHint') }}</p>

        <div>
          <p class="text-xs text-muted mb-1.5">{{ t('tiering.policy.field.formats') }}</p>
          <div class="flex flex-wrap gap-1.5" role="group" :aria-label="t('tiering.policy.field.formats')">
            <button
              v-for="fmt in formatOptions"
              :key="fmt"
              type="button"
              :aria-pressed="form.formats.includes(fmt)"
              class="rounded-md border px-0.5 py-0.5 focus-visible:ring-2 focus-visible:ring-primary transition-colors"
              :class="form.formats.includes(fmt) ? 'border-primary bg-primary-soft/40' : 'border-border opacity-70 hover:opacity-100'"
              :data-testid="`tp-format-${fmt}`"
              @click="toggleFormat(fmt)"
            >
              <FormatBadge :format="fmt" size="sm" />
            </button>
          </div>
        </div>
      </fieldset>

      <div class="flex items-center justify-between gap-3">
        <p id="tp-enabled-label" class="text-xs font-semibold text-foreground">{{ t('tiering.policy.field.enabled') }}</p>
        <AppToggle v-model="form.enabled" labelledby="tp-enabled-label" />
      </div>
    </form>

    <p v-if="formError" class="mt-4 text-caption text-muted" data-testid="tp-form-error">{{ t(formError) }}</p>

    <div v-if="dry.loading.value || dry.error.value || dry.result.value" class="mt-5 border-t border-border pt-4 space-y-3" aria-live="polite">
      <p v-if="dry.loading.value" class="text-xs text-muted">{{ t('tiering.dryRun.running') }}</p>
      <p v-else-if="dry.error.value" class="text-xs text-danger" role="alert">{{ dry.error.value }}</p>
      <TierDryRunResult v-else-if="dry.result.value" :result="dry.result.value" />
    </div>

    <template #footer>
      <AppButton
        variant="secondary"
        class="mr-auto"
        :loading="dry.loading.value"
        :disabled="!!formError"
        data-testid="tp-simulate"
        @click="simulate"
      >
        {{ t('tiering.dryRun.simulate') }}
      </AppButton>
      <AppButton variant="ghost" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="tier-policy-form" :loading="saving" :disabled="!!formError" data-testid="tp-save">
        {{ t('common.save') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import FormatBadge from './FormatBadge.vue'
import TierDryRunResult from './TierDryRunResult.vue'
import { createPolicy, updatePolicy, type StoragePolicy, type StorageTier } from '../api/tiering'
import { useTierDryRun } from '../composables/useTierDryRun'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'
import { ALL_FORMATS } from '../lib/formats'
import {
  TIER_SIZE_UNITS,
  emptyPolicyForm,
  formFromPolicy,
  formToInput,
  policyFormError,
  type PolicyForm,
} from '../lib/tiering'

const props = defineProps<{
  open: boolean
  /** The policy being edited; null creates a new one. */
  policy: StoragePolicy | null
  tiers: StorageTier[]
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const inputClass =
  'w-full h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm placeholder:text-muted focus:ring-2 focus:ring-primary'

const toasts = useToastStore()
const dry = useTierDryRun()

const form = reactive<PolicyForm>(emptyPolicyForm())
const saving = ref(false)

const tierNames = computed(() => props.tiers.flatMap((tier) => (tier.name ? [tier.name] : [])))
const formError = computed(() => policyFormError(form))
const formatOptions = computed(() => [...ALL_FORMATS, ...form.formats.filter((f) => !(ALL_FORMATS as string[]).includes(f))])
const sizeFields = computed(() => [
  { id: 'tp-min-size', label: t('tiering.policy.field.minSize'), model: form.minSize },
  { id: 'tp-max-size', label: t('tiering.policy.field.maxSize'), model: form.maxSize },
])

watch(
  () => props.open,
  (open) => {
    if (!open) return
    // The coldest tier (last in the hottest-first list) is the usual destination.
    const draft = props.policy ? formFromPolicy(props.policy) : emptyPolicyForm(tierNames.value.at(-1) ?? '')
    Object.assign(form, draft)
    dry.reset()
  },
  { immediate: true },
)

// A result describes the draft it was computed from; any edit makes it stale.
watch(form, () => dry.reset(), { deep: true })

function toggleFormat(fmt: string): void {
  const i = form.formats.indexOf(fmt)
  if (i === -1) form.formats.push(fmt)
  else form.formats.splice(i, 1)
}

async function simulate(): Promise<void> {
  if (formError.value) return
  await dry.run(formToInput(form))
}

async function save(): Promise<void> {
  if (saving.value || formError.value) return
  saving.value = true
  try {
    const body = formToInput(form)
    if (props.policy?.id) await updatePolicy(props.policy.id, body)
    else await createPolicy(body)
    toasts.success(`${t('tiering.policy.saved')}: ${body.name}`)
    emit('saved')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tiering.policy.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
