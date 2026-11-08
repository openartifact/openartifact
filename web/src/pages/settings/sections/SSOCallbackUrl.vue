<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <span class="block text-sm font-medium text-foreground mb-2">{{ kind === 'oidc' ? t('adm.sso.callback.oidc') : t('adm.sso.callback.saml') }}</span>
    <div class="flex items-center gap-1 rounded-lg border border-border-strong bg-bg pl-3">
      <code class="min-w-0 flex-1 truncate py-2 font-mono text-sm text-foreground" data-testid="sso-callback-url" :title="url">{{ url }}</code>
      <CopyButton :text="url" :label="t('adm.sso.callback.copy')" />
    </div>
    <p class="text-xs text-muted mt-1">{{ exact ? t('adm.sso.callback.hintSaved') : t('adm.sso.callback.hintNew') }}</p>
  </div>
</template>

<script setup lang="ts">
import CopyButton from '../../../components/ui/CopyButton.vue'
import { t } from '../../../i18n'

/** `exact`: the URL comes from the server (built from BASE_URL); otherwise it is computed from this page's origin. */
defineProps<{ url: string; kind: 'oidc' | 'saml'; exact: boolean }>()
</script>
