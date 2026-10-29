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
  <section class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('apkKey.title')" :aria-busy="signing.loading">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h2 class="text-sm font-semibold text-foreground">{{ t('apkKey.title') }}</h2>
        <p class="mt-1 max-w-xl text-sm text-muted">{{ t('apkKey.hint') }}</p>
      </div>
      <AppButton variant="secondary" size="sm" :disabled="signing.loading" v-if="!signing.secretRequired" data-testid="apk-key-replace" @click="signing.open = true">
        {{ t('apkKey.replace') }}
      </AppButton>
    </div>

    <p v-if="signing.loading" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <p v-else-if="signing.secretRequired" role="alert" class="text-sm text-danger" data-testid="apk-key-secret-required">{{ signing.loadError }}</p>
    <p v-else-if="signing.loadError" role="alert" class="text-sm text-danger">{{ signing.loadError }}</p>
    <template v-else-if="signing.key">
      <dl class="grid gap-3 text-xs sm:grid-cols-2" data-testid="apk-key-details">
        <div class="min-w-0">
          <dt class="text-muted">{{ t('apkKey.name') }}</dt>
          <dd class="break-all font-mono text-foreground">{{ signing.key.key_name }}</dd>
        </div>
        <div class="min-w-0">
          <dt class="text-muted">{{ t('apkKey.fingerprint') }}</dt>
          <dd class="flex items-center gap-1">
            <span class="break-all font-mono text-foreground">{{ signing.key.fingerprint }}</span>
            <CopyButton :text="signing.key.fingerprint" :label="t('apkKey.copyFingerprint')" />
          </dd>
        </div>
      </dl>
      <div class="space-y-1">
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-xs font-semibold text-foreground">{{ t('apkKey.publicKey') }}</h3>
          <div class="flex items-center gap-1">
            <CopyButton :text="signing.key.public_key" :label="t('apkKey.copyPublicKey')">{{ t('apkKey.copy') }}</CopyButton>
            <AppButton variant="secondary" size="sm" data-testid="apk-key-download" @click="download">{{ t('apkKey.download') }}</AppButton>
          </div>
        </div>
        <pre class="overflow-x-auto rounded-md border border-border bg-bg p-3 text-xs text-foreground" data-testid="apk-key-pem">{{ signing.key.public_key }}</pre>
      </div>
    </template>

    <ConfirmDialog
      :open="signing.open"
      :title="t('apkKey.dialog.title')"
      :description="t('apkKey.dialog.warning')"
      :confirm-label="signing.privateKey.trim() ? t('apkKey.dialog.import') : t('apkKey.dialog.generate')"
      :busy="signing.saving"
      :confirm-disabled="!signing.nameValid"
      @confirm="signing.replace"
      @close="signing.close"
    >
      <div class="space-y-3">
        <FormField :label="t('apkKey.dialog.keyName')" id="apk-key-name" :hint="t('apkKey.dialog.keyNameHint')" :error="signing.nameValid ? '' : t('apkKey.dialog.keyNameInvalid')">
          <input id="apk-key-name" v-model.trim="signing.keyName" type="text" autocomplete="off" placeholder="openartifact-name.rsa.pub" :class="INPUT_CLASS" />
        </FormField>
        <FormField :label="t('apkKey.dialog.privateKey')" id="apk-key-private" :hint="t('apkKey.dialog.privateKeyHint')">
          <textarea id="apk-key-private" v-model="signing.privateKey" rows="5" spellcheck="false" autocomplete="off" :class="[INPUT_CLASS, 'font-mono text-xs']" />
        </FormField>
        <p v-if="signing.saveError" role="alert" class="text-xs text-danger">{{ signing.saveError }}</p>
      </div>
    </ConfirmDialog>
  </section>
</template>

<script setup lang="ts">
import { reactive } from 'vue'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import FormField from '../FormField.vue'
import CopyButton from '../ui/CopyButton.vue'
import { getApkKey, replaceApkKey } from '../../api/apk'
import { useSigningKey } from '../../composables/useSigningKey'
import { INPUT_CLASS } from '../../lib/formClasses'
import { t } from '../../i18n'

const props = defineProps<{ repo: string }>()

// Mirrors the server's key name rule; the server stays authoritative.
const signing = reactive(useSigningKey({
  message: (which) => t(`apkKey.${which}`),
  nameRule: /^[A-Za-z0-9._@+-]{1,128}\.rsa\.pub$/,
  get: () => getApkKey(props.repo),
  replace: (body) => replaceApkKey(props.repo, body),
}))

/** Saves the PEM as `<key name>`, the file clients keep in /etc/apk/keys. */
function download(): void {
  if (!signing.key) return
  const url = URL.createObjectURL(new Blob([signing.key.public_key], { type: 'application/x-pem-file' }))
  const a = document.createElement('a')
  a.href = url
  a.download = signing.key.key_name
  a.click()
  URL.revokeObjectURL(url)
}
</script>
