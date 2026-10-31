<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <Drawer :open="open" :busy="busy" :title="t('sa.create.title')" :description="t('sa.create.desc')" @close="emit('close')">
    <form id="service-account-form" class="flex flex-col gap-5" novalidate @submit.prevent="submit">
      <FormField :label="t('sa.field.name')" id="sa-name" :error="nameError" :hint="t('sa.field.nameHint')">
        <input
          id="sa-name"
          v-model="name"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :class="[INPUT_CLASS, 'font-mono']"
          :aria-invalid="nameError ? 'true' : undefined"
          :aria-describedby="nameError ? 'sa-name-error' : undefined"
        />
      </FormField>
      <FormField :label="t('sa.field.description')" id="sa-description" :hint="t('sa.field.descriptionHint')">
        <input id="sa-description" v-model="description" type="text" maxlength="255" autocomplete="off" :class="INPUT_CLASS" />
      </FormField>
      <p v-if="error" role="alert" class="text-sm text-danger" data-testid="create-error">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="ghost" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="service-account-form" :loading="busy">{{ t('common.create') }}</AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import { createServiceAccount, type ServiceAccountItem } from '../../api/serviceAccounts'
import { INPUT_CLASS } from '../../lib/formClasses'
import { SERVICE_ACCOUNT_NAME } from '../../lib/serviceAccounts'
import { t } from '../../i18n'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; created: [account: ServiceAccountItem] }>()

const name = ref('')
const description = ref('')
const touched = ref(false)
const busy = ref(false)
const error = ref('')

const nameError = computed(() => (touched.value && !SERVICE_ACCOUNT_NAME.test(name.value.trim()) ? t('sa.field.nameInvalid') : undefined))

// A fresh form each time the drawer opens.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    name.value = ''
    description.value = ''
    touched.value = false
    error.value = ''
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  touched.value = true
  if (busy.value || !SERVICE_ACCOUNT_NAME.test(name.value.trim())) return
  busy.value = true
  error.value = ''
  try {
    const created = await createServiceAccount({ name: name.value.trim(), description: description.value.trim() || undefined })
    emit('created', created)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('sa.error.failed')
  } finally {
    busy.value = false
  }
}
</script>
