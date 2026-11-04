<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog drawer max-width="max-w-xl" :open="open" :eyebrow="t('adm.gpg.eyebrow')" :title="t('admin.gpg.generate')" @close="emit('close')">
    <form id="gpg-generate-form" class="space-y-5" @submit.prevent="submitGenerate">
      <FormField id="gpg-name" :label="`${t('adm.gpg.name')} (${t('common.optional')})`" :hint="t('adm.gpg.nameHint')">
        <input id="gpg-name" v-model="genForm.name" type="text" :class="inputClass" />
      </FormField>
      <FormField id="gpg-algorithm" :label="t('adm.gpg.algorithm')">
        <select id="gpg-algorithm" v-model="genForm.algorithm" :class="inputClass">
          <option v-for="algo in ALGORITHMS" :key="algo" :value="algo">{{ algo }}</option>
        </select>
      </FormField>
      <FormField id="gpg-user-name" :label="`${t('adm.gpg.userName')} (${t('common.optional')})`">
        <input id="gpg-user-name" v-model="genForm.userName" type="text" :class="inputClass" />
      </FormField>
      <FormField id="gpg-email" :label="`${t('adm.gpg.email')} (${t('common.optional')})`">
        <input id="gpg-email" v-model="genForm.email" type="email" :class="inputClass" />
      </FormField>
      <FormField id="gpg-comment" :label="`${t('adm.gpg.comment')} (${t('common.optional')})`">
        <input id="gpg-comment" v-model="genForm.comment" type="text" :class="inputClass" />
      </FormField>
      <GpgActivateToggle v-model="genForm.activate" />
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="gpg-generate-form" :loading="busy || submitting">
        {{ t('admin.gpg.generate') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import GpgActivateToggle from './GpgActivateToggle.vue'
import { ref, watch } from 'vue'
import AdminDialog from '../../../components/AdminDialog.vue'
import AppButton from '../../../components/AppButton.vue'
import FormField from '../../../components/FormField.vue'
import { generateGpgKey, type GpgAlgorithm } from '../../../api/gpg'
import { errorMessage, inputClass } from '../../../lib/gpgForm'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n'

const ALGORITHMS: GpgAlgorithm[] = ['ed25519', 'rsa4096']

const props = defineProps<{ open: boolean; busy: boolean }>()
const emit = defineEmits<{ close: []; done: [] }>()

const toasts = useToastStore()

const emptyGenerate = () => ({ name: '', algorithm: 'ed25519' as GpgAlgorithm, userName: '', email: '', comment: '', activate: false })
const genForm = ref(emptyGenerate())
const submitting = ref(false)

watch(
  () => props.open,
  () => {
    genForm.value = emptyGenerate()
  },
)

async function submitGenerate(): Promise<void> {
  if (props.busy || submitting.value) return
  submitting.value = true
  const f = genForm.value
  try {
    await generateGpgKey({
      name: f.name.trim() || undefined,
      algorithm: f.algorithm,
      user_name: f.userName.trim() || undefined,
      email: f.email.trim() || undefined,
      comment: f.comment.trim() || undefined,
      activate: f.activate || undefined,
    })
    toasts.success(t('adm.gpg.generated'))
    emit('done')
  } catch (err) {
    toasts.error(errorMessage(err, t('adm.gpg.generateFailed')))
  } finally {
    submitting.value = false
  }
}
</script>
