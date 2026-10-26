<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Create a global license policy in a drawer. The server cannot edit or delete global policies yet. -->
<template>
  <Drawer
    :open="open"
    :eyebrow="t('sx.policies.drawer.eyebrow')"
    :title="t('sx.policies.drawer.title')"
    :busy="busy"
    @close="emit('close')"
  >
    <form id="license-policy-form" class="flex flex-col gap-5" novalidate @submit.prevent="submit">
      <FormField :label="t('sx.policies.field.name')" id="lp-name" :error="nameError">
        <input
          id="lp-name"
          v-model="name"
          type="text"
          autocomplete="off"
          :placeholder="t('sx.policies.field.namePlaceholder')"
          :class="INPUT_CLASS"
          :aria-invalid="nameError ? 'true' : undefined"
          :aria-describedby="nameError ? 'lp-name-error' : undefined"
        />
      </FormField>

      <FormField :label="t('sx.policies.field.description')" id="lp-description">
        <input id="lp-description" v-model="description" type="text" autocomplete="off" :class="INPUT_CLASS" />
      </FormField>

      <FormField :label="t('sx.policies.field.action')" id="lp-action" :hint="t('sx.policies.field.actionHint')">
        <select id="lp-action" v-model="action" :class="INPUT_CLASS">
          <option v-for="a in LICENSE_POLICY_ACTIONS" :key="a" :value="a">{{ t(`sx.policies.act.${a}`) }}</option>
        </select>
      </FormField>

      <FormField :label="t('sx.policies.field.denied')" id="lp-denied" :hint="t('sx.policies.field.licensesHint')">
        <textarea id="lp-denied" v-model="denied" rows="3" :class="[INPUT_CLASS, 'font-mono']" />
      </FormField>

      <FormField :label="t('sx.policies.field.allowed')" id="lp-allowed" :hint="t('sx.policies.field.licensesHint')">
        <textarea id="lp-allowed" v-model="allowed" rows="3" :class="[INPUT_CLASS, 'font-mono']" />
      </FormField>

      <label class="flex min-h-11 items-center gap-2 text-sm text-foreground">
        <input v-model="enabled" type="checkbox" />
        {{ t('sx.policies.field.enabled') }}
      </label>

      <p v-if="error" role="alert" class="text-sm text-danger" data-testid="policy-error">{{ error }}</p>
      <p class="text-xs text-muted">{{ t('sx.policies.editNote') }}</p>
    </form>
    <template #footer>
      <AppButton variant="ghost" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="license-policy-form" :loading="busy">{{ t('sx.policies.new') }}</AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import Drawer from './ui/Drawer.vue'
import { createGlobalLicensePolicy, LICENSE_POLICY_ACTIONS, type LicensePolicy, type LicensePolicyAction } from '../api/licensePolicies'
import { INPUT_CLASS } from '../lib/formClasses'
import { licensePolicyNameProblem, parseLicenseList } from '../lib/licensePolicy'
import { t } from '../i18n/en'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; created: [LicensePolicy] }>()

const name = ref('')
const description = ref('')
const action = ref<LicensePolicyAction>('warn')
const denied = ref('')
const allowed = ref('')
const enabled = ref(true)
const touched = ref(false)
const busy = ref(false)
const error = ref('')

const problem = computed(() => licensePolicyNameProblem(name.value))
const nameError = computed(() => {
  if (!touched.value || !problem.value) return undefined
  return problem.value === 'required' ? t('sx.policies.nameRequired') : t('sx.policies.nameTooLong')
})

// A fresh form each time the drawer opens.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    name.value = ''
    description.value = ''
    action.value = 'warn'
    denied.value = ''
    allowed.value = ''
    enabled.value = true
    touched.value = false
    error.value = ''
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  touched.value = true
  if (problem.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const created = await createGlobalLicensePolicy({
      name: name.value.trim(),
      description: description.value.trim(),
      action: action.value,
      allowed_licenses: parseLicenseList(allowed.value),
      denied_licenses: parseLicenseList(denied.value),
      enabled: enabled.value,
    })
    emit('created', created)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('sx.policies.createFailed')
  } finally {
    busy.value = false
  }
}
</script>
