<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <div class="flex items-center justify-between gap-2 mb-2">
      <span id="sso-dex-snippet-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.dexSnippet.title') }}</span>
      <CopyButton :text="snippet" :label="t('adm.sso.dexSnippet.copy')" />
    </div>
    <pre
      class="max-h-72 overflow-auto rounded-lg border border-border-strong bg-bg p-3 font-mono text-xs text-foreground"
      tabindex="0"
      aria-labelledby="sso-dex-snippet-label"
      data-testid="sso-dex-snippet"
    >{{ snippet }}</pre>
    <p class="text-xs text-muted mt-1">{{ t('adm.sso.dexSnippet.hint') }}</p>
    <DocsLink :url="DEX_CONNECTORS[oidc.dex_connector_type].docs" class="mt-1">{{ t('adm.sso.dexSnippet.docs') }}</DocsLink>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import CopyButton from '../../../components/ui/CopyButton.vue'
import DocsLink from '../../../components/DocsLink.vue'
import { t } from '../../../i18n'
import { dexSnippet } from '../../../lib/dexSnippet'
import { DEX_CONNECTORS } from '../../../lib/ssoPresets'
import type { SSOFormState } from '../../../lib/ssoProviderForm'

const props = defineProps<{ form: SSOFormState; callbackUrl: string }>()

const oidc = computed(() => props.form.oidc_config)

const snippet = computed(() =>
  dexSnippet({
    connectorType: oidc.value.dex_connector_type,
    connectorId: oidc.value.dex_connector_id,
    clientId: oidc.value.client_id,
    displayName: props.form.display_name,
    callbackUrl: props.callbackUrl,
    dexIssuer: oidc.value.issuer,
  }),
)
</script>
