<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <Drawer :open="open" :busy="busy" :title="t('sa.token.createTitle')" :description="t('sa.token.createDesc')" @close="emit('close')">
    <form id="account-token-form" class="flex flex-col gap-5" novalidate @submit.prevent="submit">
      <FormField :label="t('tok.create.name')" id="sa-token-name" :error="nameError" :hint="t('tok.create.nameHint')">
        <input id="sa-token-name" v-model="name" type="text" autocomplete="off" :class="INPUT_CLASS" :aria-invalid="nameError ? 'true' : undefined" />
      </FormField>
      <FormField :label="t('tok.create.kind')" id="sa-token-kind" :hint="tDynamic(`sa.token.kind.${kind}.hint`)">
        <select id="sa-token-kind" v-model="kind" :class="INPUT_CLASS">
          <option value="deploy">{{ t('sa.token.kind.deploy') }}</option>
          <option value="access">{{ t('sa.token.kind.access') }}</option>
        </select>
      </FormField>
      <FormField :label="t('tok.create.expires')" id="sa-token-expiry">
        <select id="sa-token-expiry" v-model.number="days" :class="INPUT_CLASS">
          <option v-for="d in EXPIRY_PRESETS" :key="d" :value="d">{{ d === 0 ? t('tok.expiry.never') : tp('tok.expiry.days', { count: String(d) }) }}</option>
        </select>
        <p v-if="days === 0" class="text-xs text-warning" data-testid="no-expiry-warning">{{ t('tok.create.noExpiryWarning') }}</p>
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-danger" data-testid="token-error">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="ghost" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="account-token-form" :loading="busy">{{ t('tok.create.submit') }}</AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import { createUserToken } from '../../api/tokens'
import { INPUT_CLASS } from '../../lib/formClasses'
import { DEFAULT_EXPIRY_DAYS, EXPIRY_PRESETS, expiresAtFor, nameProblem } from '../../lib/tokens'
import { t, tDynamic, tp } from '../../i18n'

/** The one-time result of a creation: the page shows the secret once. */
export interface AccountTokenSecret {
  secret: string
  name: string
}

const props = defineProps<{ open: boolean; accountId: string }>()
const emit = defineEmits<{ close: []; created: [AccountTokenSecret] }>()

// A service account is for automation: the deploy kind (artifacts only) is the default.
const name = ref('')
const kind = ref<'access' | 'deploy'>('deploy')
const days = ref(DEFAULT_EXPIRY_DAYS)
const touched = ref(false)
const busy = ref(false)
const error = ref('')

const problem = computed(() => nameProblem('personal', name.value))
const nameError = computed(() => (touched.value && problem.value ? tDynamic(`tok.create.nameError.${problem.value}`) : undefined))

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    name.value = ''
    kind.value = 'deploy'
    days.value = DEFAULT_EXPIRY_DAYS
    touched.value = false
    error.value = ''
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  touched.value = true
  if (busy.value || problem.value) return
  busy.value = true
  error.value = ''
  const trimmed = name.value.trim()
  try {
    const res = await createUserToken(props.accountId, { name: trimmed, kind: kind.value, expires_at: expiresAtFor(days.value) })
    emit('created', { secret: res.token ?? '', name: trimmed })
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('tok.create.failed')
  } finally {
    busy.value = false
  }
}
</script>
