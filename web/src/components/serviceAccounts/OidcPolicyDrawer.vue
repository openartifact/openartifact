<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Create or edit a CI OIDC trust policy. The server's validation message (400, 409) is shown as it comes. -->
<template>
  <Drawer
    :open="open"
    :busy="busy"
    wide
    :title="policy ? t('sa.policy.editTitle') : t('sa.policy.createTitle')"
    :description="t('sa.policy.drawerDesc')"
    @close="emit('close')"
  >
    <form id="oidc-policy-form" class="flex flex-col gap-5" novalidate @submit.prevent="submit">
      <div v-if="!policy" class="flex flex-wrap items-center gap-2" data-testid="policy-presets">
        <span class="text-sm font-medium text-foreground">{{ t('sa.preset.label') }}</span>
        <AppButton v-for="p in PRESETS" :key="p.id" variant="secondary" size="sm" :data-testid="`preset-${p.id}`" @click="applyPreset(p)">
          {{ tDynamic(`sa.preset.${p.id}`) }}
        </AppButton>
      </div>

      <FormField :label="t('sa.policy.name')" id="policy-name" :error="fieldError('name')" :hint="t('sa.policy.nameHint')">
        <input id="policy-name" v-model="form.name" type="text" autocomplete="off" spellcheck="false" :class="[INPUT_CLASS, 'font-mono']" :aria-invalid="fieldError('name') ? 'true' : undefined" />
      </FormField>

      <FormField :label="t('sa.policy.issuer')" id="policy-issuer" :error="fieldError('issuer')" :hint="issuerHint">
        <input
          id="policy-issuer"
          v-model="form.issuer"
          type="url"
          autocomplete="off"
          spellcheck="false"
          placeholder="https://token.actions.githubusercontent.com"
          :class="[INPUT_CLASS, 'font-mono']"
          :aria-invalid="fieldError('issuer') ? 'true' : undefined"
        />
      </FormField>

      <FormField :label="t('sa.policy.audience')" id="policy-audience" :error="fieldError('audience')" :hint="t('sa.policy.audienceHint')">
        <input id="policy-audience" v-model="form.audience" type="text" autocomplete="off" spellcheck="false" :class="[INPUT_CLASS, 'font-mono']" :aria-invalid="fieldError('audience') ? 'true' : undefined" />
      </FormField>

      <OidcConditionsEditor v-model:rows="form.rows" />
      <p v-if="fieldError('conditions')" role="alert" class="-mt-3 text-xs text-danger">{{ fieldError('conditions') }}</p>

      <FormField :label="t('sa.policy.account')" id="policy-account" :error="fieldError('account')" :hint="t('sa.policy.accountHint')">
        <select id="policy-account" v-model="form.serviceAccountId" :class="INPUT_CLASS" :aria-invalid="fieldError('account') ? 'true' : undefined">
          <option value="" disabled>{{ t('sa.policy.accountPick') }}</option>
          <option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.name }}</option>
        </select>
      </FormField>

      <FormField :label="t('sa.policy.ttl')" id="policy-ttl" :error="fieldError('ttl')" :hint="tp('sa.policy.ttlHint', { min: String(MIN_TTL_MINUTES), max: String(MAX_TTL_MINUTES) })">
        <div class="flex items-center gap-2">
          <input
            id="policy-ttl"
            v-model.number="form.ttlMinutes"
            type="number"
            :min="MIN_TTL_MINUTES"
            :max="MAX_TTL_MINUTES"
            step="1"
            :class="[INPUT_CLASS, 'max-w-28']"
            :aria-invalid="fieldError('ttl') ? 'true' : undefined"
          />
          <span class="text-sm text-muted">{{ t('sa.policy.minutes') }}</span>
        </div>
      </FormField>

      <div class="flex items-center gap-3">
        <AppToggle v-model="form.enabled" labelledby="policy-enabled-label" />
        <span id="policy-enabled-label" class="text-sm text-foreground">{{ t('sa.policy.enabled') }}</span>
      </div>

      <p v-if="error" role="alert" class="text-sm text-danger" data-testid="policy-error">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="ghost" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="oidc-policy-form" :loading="busy">{{ policy ? t('common.save') : t('common.create') }}</AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import AppToggle from '../AppToggle.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import OidcConditionsEditor from './OidcConditionsEditor.vue'
import { createOidcPolicy, updateOidcPolicy, type OidcPolicy, type ServiceAccountItem } from '../../api/serviceAccounts'
import { INPUT_CLASS } from '../../lib/formClasses'
import {
  blankForm,
  formOf,
  inputOf,
  MAX_TTL_MINUTES,
  MIN_TTL_MINUTES,
  policyProblem,
  presetForm,
  presetOfIssuer,
  PRESETS,
  type PolicyForm,
  type PolicyProblem,
  type Preset,
} from '../../lib/oidcPolicies'
import { t, tDynamic, tp } from '../../i18n'

const props = defineProps<{
  open: boolean
  /** The policy to edit; null creates one. */
  policy: OidcPolicy | null
  accounts: ServiceAccountItem[]
  /** Service account a new policy starts on (the account page). */
  accountId?: string
}>()
const emit = defineEmits<{ close: []; saved: [policy: OidcPolicy] }>()

const form = reactive<PolicyForm>(blankForm())
const touched = ref(false)
const busy = ref(false)
const error = ref('')

function load(next: PolicyForm): void {
  Object.assign(form, next)
  touched.value = false
  error.value = ''
}

// A fresh form each time the drawer opens.
watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) load(props.policy ? formOf(props.policy) : blankForm(props.accountId))
  },
  { immediate: true },
)

function applyPreset(preset: Preset): void {
  const { name, audience } = form
  load({ ...presetForm(preset, form.serviceAccountId), name, audience })
}

const issuerHint = computed(() => (presetOfIssuer(form.issuer) ? t('sa.policy.issuerShared') : t('sa.policy.issuerHint')))

const FIELD_OF: Record<PolicyProblem, string> = {
  name: 'name',
  issuer: 'issuer',
  audience: 'audience',
  account: 'account',
  ttl: 'ttl',
  noConditions: 'conditions',
  claim: 'conditions',
  patterns: 'conditions',
  tooManyPatterns: 'conditions',
}
const problem = computed(() => policyProblem(form))
function fieldError(field: string): string | undefined {
  const p = problem.value
  return touched.value && p && FIELD_OF[p] === field ? tDynamic(`sa.policy.problem.${p}`) : undefined
}

async function submit(): Promise<void> {
  touched.value = true
  if (busy.value || problem.value) return
  busy.value = true
  error.value = ''
  try {
    const body = inputOf(form)
    const saved = props.policy ? await updateOidcPolicy(props.policy.id, body) : await createOidcPolicy(body)
    emit('saved', saved)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('sa.error.failed')
  } finally {
    busy.value = false
  }
}
</script>
