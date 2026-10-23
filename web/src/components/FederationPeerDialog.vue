<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Add or edit a federation peer. Secrets are never returned by the API, so the
  edit form starts with empty secret fields and only sends what was typed.
  Emits `submit` with the validated form; the parent performs the request and
  closes the dialog.
-->
<template>
  <AdminDialog :open="open" :title="title" max-width="max-w-xl" @close="close">
    <form id="federation-peer-form" class="space-y-4" novalidate @submit.prevent="submit">
      <FormField :label="t('federation.peerDialog.name')" id="fed-peer-name" :hint="t('federation.peerDialog.nameHint')" :error="shown.name">
        <input id="fed-peer-name" v-model="form.name" type="text" autocomplete="off" :class="INPUT_CLASS" />
      </FormField>
      <FormField :label="t('federation.peerDialog.url')" id="fed-peer-url" :hint="t('federation.peerDialog.urlHint')" :error="shown.baseUrl">
        <input id="fed-peer-url" v-model="form.baseUrl" type="url" autocomplete="off" placeholder="https://" :class="[INPUT_CLASS, 'font-mono']" />
      </FormField>
      <FormField
        :label="t('federation.peerDialog.token')"
        id="fed-peer-token"
        :hint="isEdit ? t('federation.peerDialog.tokenKeep') : t('federation.peerDialog.tokenHint')"
        :error="shown.token"
      >
        <input id="fed-peer-token" v-model="form.token" type="password" autocomplete="new-password" :class="[INPUT_CLASS, 'font-mono']" />
      </FormField>

      <fieldset class="space-y-4 rounded-lg border border-border p-4">
        <legend class="px-1 text-xs font-semibold text-muted">{{ t('federation.peerDialog.tls') }}</legend>
        <p v-if="isEdit" class="text-xs text-muted">{{ t('federation.peerDialog.tlsKeep') }}</p>
        <FormField :label="t('federation.peerDialog.ca')" id="fed-peer-ca" :hint="peer?.has_ca_pem ? t('federation.peerDialog.caStored') : undefined">
          <textarea id="fed-peer-ca" v-model="form.caPem" rows="3" spellcheck="false" :class="[INPUT_CLASS, 'font-mono text-xs']" />
        </FormField>
        <FormField
          :label="t('federation.peerDialog.clientCert')"
          id="fed-peer-cert"
          :hint="peer?.has_client_cert ? t('federation.peerDialog.clientCertStored') : undefined"
          :error="shown.clientCert"
        >
          <textarea id="fed-peer-cert" v-model="form.clientCert" rows="3" spellcheck="false" :class="[INPUT_CLASS, 'font-mono text-xs']" />
        </FormField>
        <FormField :label="t('federation.peerDialog.clientKey')" id="fed-peer-key">
          <textarea id="fed-peer-key" v-model="form.clientKey" rows="3" spellcheck="false" autocomplete="off" :class="[INPUT_CLASS, 'font-mono text-xs']" />
        </FormField>
      </fieldset>

      <div v-if="isEdit" class="flex items-center gap-3">
        <input id="fed-peer-enabled" v-model="form.enabled" type="checkbox" class="rounded accent-[rgb(var(--color-primary))]" />
        <label for="fed-peer-enabled" class="text-sm font-medium text-foreground">{{ t('federation.peerDialog.enabled') }}</label>
      </div>
    </form>
    <template #footer>
      <AppButton variant="ghost" :disabled="busy" @click="close">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="federation-peer-form" :loading="busy">
        {{ isEdit ? t('common.save') : t('federation.peerDialog.add') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import type { FederationPeer } from '../api/federation'
import { emptyPeerForm, peerFormFrom, validatePeerForm, type PeerForm, type PeerFormErrors } from '../lib/federation'
import { INPUT_CLASS } from '../lib/formClasses'
import { t } from '../i18n/en'

const props = defineProps<{
  open: boolean
  /** The peer being edited; null adds a new one. */
  peer: FederationPeer | null
  busy?: boolean
}>()
const emit = defineEmits<{ close: []; submit: [form: PeerForm] }>()

const form = ref<PeerForm>(emptyPeerForm())
const submitted = ref(false)

const isEdit = computed(() => props.peer !== null)
const title = computed(() => (isEdit.value ? t('federation.peerDialog.editTitle') : t('federation.peerDialog.addTitle')))
const errors = computed<PeerFormErrors>(() => validatePeerForm(form.value, isEdit.value ? 'edit' : 'create'))
// Errors appear once the user tried to submit, not while typing the first field.
const shown = computed<PeerFormErrors>(() => (submitted.value ? errors.value : {}))

// A fresh form every time the dialog opens.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    form.value = props.peer ? peerFormFrom(props.peer) : emptyPeerForm()
    submitted.value = false
  },
  { immediate: true },
)

function close(): void {
  if (!props.busy) emit('close')
}

function submit(): void {
  submitted.value = true
  if (Object.keys(errors.value).length > 0) return
  emit('submit', form.value)
}
</script>
