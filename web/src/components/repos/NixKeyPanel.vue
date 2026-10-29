<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The narinfo signing key of a hosted nix repository: clients add the public key to trusted-public-keys.
  Maintainers can generate a new key or import one made by nix-store --generate-binary-cache-key; narinfo
  are signed when served, so every client then has to trust the new public key.
-->
<template>
  <section class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('nixKey.title')" :aria-busy="signing.loading">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h2 class="text-sm font-semibold text-foreground">{{ t('nixKey.title') }}</h2>
        <p class="mt-1 max-w-xl text-sm text-muted">{{ t('nixKey.hint') }}</p>
      </div>
      <AppButton v-if="!signing.secretRequired" variant="secondary" size="sm" :disabled="signing.loading" data-testid="nix-key-replace" @click="signing.open = true">
        {{ t('nixKey.replace') }}
      </AppButton>
    </div>

    <p v-if="signing.loading" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <p v-else-if="signing.loadError" role="alert" class="text-sm text-danger" :data-testid="signing.secretRequired ? 'nix-key-secret-required' : undefined">{{ signing.loadError }}</p>
    <template v-else-if="signing.key">
      <div class="min-w-0 text-xs">
        <p class="text-muted">{{ t('nixKey.name') }}</p>
        <p class="break-all font-mono text-foreground" data-testid="nix-key-name">{{ signing.key.key_name }}</p>
      </div>
      <div class="space-y-1">
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-xs font-semibold text-foreground">{{ t('nixKey.publicKey') }}</h3>
          <CopyButton :text="signing.key.public_key" :label="t('nixKey.copyPublicKey')">{{ t('nixKey.copy') }}</CopyButton>
        </div>
        <pre class="overflow-x-auto rounded-md border border-border bg-bg p-3 text-xs text-foreground" data-testid="nix-key-public">{{ signing.key.public_key }}</pre>
      </div>
    </template>

    <ConfirmDialog
      :open="signing.open"
      :title="t('nixKey.dialog.title')"
      :description="t('nixKey.dialog.warning')"
      :confirm-label="signing.privateKey.trim() ? t('nixKey.dialog.import') : t('nixKey.dialog.generate')"
      :busy="signing.saving"
      :confirm-disabled="!signing.nameValid"
      @confirm="signing.replace"
      @close="signing.close"
    >
      <div class="space-y-3">
        <FormField :label="t('nixKey.dialog.keyName')" id="nix-key-name-input" :hint="t('nixKey.dialog.keyNameHint')" :error="signing.nameValid ? '' : t('nixKey.dialog.keyNameInvalid')">
          <input id="nix-key-name-input" v-model.trim="signing.keyName" type="text" autocomplete="off" placeholder="openartifact-name-1" :class="INPUT_CLASS" />
        </FormField>
        <FormField :label="t('nixKey.dialog.privateKey')" id="nix-key-private" :hint="t('nixKey.dialog.privateKeyHint')">
          <textarea id="nix-key-private" v-model="signing.privateKey" rows="3" spellcheck="false" autocomplete="off" :class="[INPUT_CLASS, 'font-mono text-xs']" />
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
import { getNixKey, replaceNixKey } from '../../api/nix'
import { useSigningKey } from '../../composables/useSigningKey'
import { INPUT_CLASS } from '../../lib/formClasses'
import { t } from '../../i18n'

const props = defineProps<{ repo: string }>()

// Mirrors the server's key name rule; the server stays authoritative.
const signing = reactive(useSigningKey({
  message: (which) => t(`nixKey.${which}`),
  nameRule: /^[A-Za-z0-9._@+-]{1,128}$/,
  get: () => getNixKey(props.repo),
  replace: (body) => replaceNixKey(props.repo, body),
}))
</script>
