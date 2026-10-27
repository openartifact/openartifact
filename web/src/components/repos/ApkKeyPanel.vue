<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The signing key of an apk repository: clients install the public key into /etc/apk/keys to verify the
  APKINDEX. Maintainers can generate a new key or import their own private key; every client then has to
  install the new public key.
-->
<template>
  <section class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('apkKey.title')" :aria-busy="loading">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h2 class="text-sm font-semibold text-foreground">{{ t('apkKey.title') }}</h2>
        <p class="mt-1 max-w-xl text-sm text-muted">{{ t('apkKey.hint') }}</p>
      </div>
      <AppButton variant="secondary" size="sm" :disabled="loading" v-if="!secretRequired" data-testid="apk-key-replace" @click="open = true">
        {{ t('apkKey.replace') }}
      </AppButton>
    </div>

    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <p v-else-if="secretRequired" role="alert" class="text-sm text-danger" data-testid="apk-key-secret-required">{{ loadError }}</p>
    <p v-else-if="loadError" role="alert" class="text-sm text-danger">{{ loadError }}</p>
    <template v-else-if="key">
      <dl class="grid gap-3 text-xs sm:grid-cols-2" data-testid="apk-key-details">
        <div class="min-w-0">
          <dt class="text-muted">{{ t('apkKey.name') }}</dt>
          <dd class="break-all font-mono text-foreground">{{ key.key_name }}</dd>
        </div>
        <div class="min-w-0">
          <dt class="text-muted">{{ t('apkKey.fingerprint') }}</dt>
          <dd class="flex items-center gap-1">
            <span class="break-all font-mono text-foreground">{{ key.fingerprint }}</span>
            <CopyButton :text="key.fingerprint" :label="t('apkKey.copyFingerprint')" />
          </dd>
        </div>
      </dl>
      <div class="space-y-1">
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-xs font-semibold text-foreground">{{ t('apkKey.publicKey') }}</h3>
          <div class="flex items-center gap-1">
            <CopyButton :text="key.public_key" :label="t('apkKey.copyPublicKey')">{{ t('apkKey.copy') }}</CopyButton>
            <AppButton variant="secondary" size="sm" data-testid="apk-key-download" @click="download">{{ t('apkKey.download') }}</AppButton>
          </div>
        </div>
        <pre class="overflow-x-auto rounded-md border border-border bg-bg p-3 text-xs text-foreground" data-testid="apk-key-pem">{{ key.public_key }}</pre>
      </div>
    </template>

    <ConfirmDialog
      :open="open"
      :title="t('apkKey.dialog.title')"
      :description="t('apkKey.dialog.warning')"
      :confirm-label="privateKey.trim() ? t('apkKey.dialog.import') : t('apkKey.dialog.generate')"
      :busy="saving"
      :confirm-disabled="!nameValid"
      @confirm="replace"
      @close="close"
    >
      <div class="space-y-3">
        <FormField :label="t('apkKey.dialog.keyName')" id="apk-key-name" :hint="t('apkKey.dialog.keyNameHint')" :error="nameValid ? '' : t('apkKey.dialog.keyNameInvalid')">
          <input id="apk-key-name" v-model.trim="keyName" type="text" autocomplete="off" placeholder="openartifact-name.rsa.pub" :class="INPUT_CLASS" />
        </FormField>
        <FormField :label="t('apkKey.dialog.privateKey')" id="apk-key-private" :hint="t('apkKey.dialog.privateKeyHint')">
          <textarea id="apk-key-private" v-model="privateKey" rows="5" spellcheck="false" autocomplete="off" :class="[INPUT_CLASS, 'font-mono text-xs']" />
        </FormField>
        <p v-if="saveError" role="alert" class="text-xs text-danger">{{ saveError }}</p>
      </div>
    </ConfirmDialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import FormField from '../FormField.vue'
import CopyButton from '../ui/CopyButton.vue'
import { getApkKey, replaceApkKey, type ApkKey, type ApkKeyInput } from '../../api/apk'
import { ApiError } from '../../api/client'
import { INPUT_CLASS } from '../../lib/formClasses'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n'

const props = defineProps<{ repo: string }>()

const toasts = useToastStore()

const key = ref<ApkKey | null>(null)
const loading = ref(true)
const loadError = ref('')
// The server cannot seal a private key without OPENARTIFACT_SECRET_KEY: no key can be generated or imported.
const secretRequired = ref(false)

// Mirrors pkg/apk's key file name rule; the server stays authoritative.
const KEY_NAME = /^[A-Za-z0-9._@+-]{1,128}\.rsa\.pub$/

const open = ref(false)
const keyName = ref('')
const privateKey = ref('')
const saving = ref(false)
const saveError = ref('')
const nameValid = computed(() => keyName.value === '' || KEY_NAME.test(keyName.value))

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  secretRequired.value = false
  try {
    key.value = await getApkKey(props.repo)
  } catch (err) {
    secretRequired.value = err instanceof ApiError && err.status === 409 && err.code === 'secret_key_required'
    loadError.value = err instanceof Error ? err.message : t('apkKey.loadFailed')
  } finally {
    loading.value = false
  }
}
onMounted(load)

function close(): void {
  if (saving.value) return
  open.value = false
  saveError.value = ''
  privateKey.value = ''
  keyName.value = ''
}

async function replace(): Promise<void> {
  const body: ApkKeyInput = {}
  if (privateKey.value.trim()) body.private_key = privateKey.value.trim()
  if (keyName.value) body.key_name = keyName.value
  saving.value = true
  saveError.value = ''
  try {
    key.value = await replaceApkKey(props.repo, body)
    loadError.value = ''
    saving.value = false
    close()
    toasts.success(t('apkKey.replaced'), t('apkKey.replacedHint'))
  } catch (err) {
    saveError.value = err instanceof Error ? err.message : t('apkKey.replaceFailed')
  } finally {
    saving.value = false
  }
}

/** Saves the PEM as `<key name>`, the file clients keep in /etc/apk/keys. */
function download(): void {
  if (!key.value) return
  const url = URL.createObjectURL(new Blob([key.value.public_key], { type: 'application/x-pem-file' }))
  const a = document.createElement('a')
  a.href = url
  a.download = key.value.key_name
  a.click()
  URL.revokeObjectURL(url)
}
</script>
