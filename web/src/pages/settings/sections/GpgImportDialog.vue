<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog drawer max-width="max-w-xl" :open="open" :eyebrow="t('adm.gpg.eyebrow')" :title="t('admin.gpg.import')" @close="emit('close')">
    <form id="gpg-import-form" class="space-y-5" autocomplete="off" @submit.prevent="submitImport">
      <FormField id="gpg-import-name" :label="`${t('adm.gpg.name')} (${t('common.optional')})`" :hint="t('adm.gpg.nameHint')">
        <input id="gpg-import-name" v-model="importForm.name" type="text" :class="inputClass" />
      </FormField>
      <FormField id="gpg-armored" :label="t('adm.gpg.armored')">
        <textarea
          id="gpg-armored"
          v-model="importForm.privateKey"
          rows="10"
          required
          spellcheck="false"
          autocomplete="off"
          class="w-full px-3 py-2 border border-border-strong rounded-lg text-xs font-mono bg-surface-hover/40 text-foreground focus:ring-2 focus:ring-primary/50"
        />
      </FormField>
      <div>
        <label for="gpg-armored-file" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.armoredFile') }}</label>
        <input
          id="gpg-armored-file"
          type="file"
          accept=".asc,.gpg,.key,.pgp,text/plain"
          class="block w-full text-sm text-muted"
          @change="loadKeyFile"
        />
      </div>
      <FormField
        v-if="passphraseNeeded"
        id="gpg-passphrase"
        :label="t('adm.gpg.passphrase')"
        :hint="t('adm.gpg.passphraseRequired')"
      >
        <input
          id="gpg-passphrase"
          v-model="importForm.passphrase"
          type="password"
          required
          autocomplete="new-password"
          :class="inputClass"
        />
      </FormField>
      <GpgActivateToggle v-model="importForm.activate" />
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="gpg-import-form" :loading="busy || submitting">
        {{ t('admin.gpg.import') }}
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
import { importGpgKey } from '../../../api/gpg'
import { ApiError } from '../../../api/client'
import { errorMessage, inputClass } from '../../../lib/gpgForm'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n'

const props = defineProps<{ open: boolean; busy: boolean }>()
const emit = defineEmits<{ close: []; done: [] }>()

const toasts = useToastStore()

// Private key material lives only in this form.
const emptyImport = () => ({ name: '', privateKey: '', passphrase: '', activate: false })
const importForm = ref(emptyImport())
const passphraseNeeded = ref(false)
const submitting = ref(false)

watch(
  () => props.open,
  () => {
    importForm.value = emptyImport()
    passphraseNeeded.value = false
  },
)

async function loadKeyFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (file) importForm.value.privateKey = await file.text()
  input.value = ''
}

async function submitImport(): Promise<void> {
  if (props.busy || submitting.value) return
  submitting.value = true
  const { name, privateKey, passphrase, activate } = importForm.value
  try {
    await importGpgKey({
      name: name.trim() || undefined,
      private_key: privateKey,
      passphrase: passphrase || undefined,
      activate: activate || undefined,
    })
    toasts.success(t('adm.gpg.imported'))
    emit('done')
  } catch (err) {
    importForm.value.passphrase = ''
    if (err instanceof ApiError && err.code === 'passphrase_required') {
      passphraseNeeded.value = true
    } else {
      toasts.error(errorMessage(err, t('adm.gpg.importFailed')))
    }
  } finally {
    submitting.value = false
  }
}
</script>
