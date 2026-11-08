<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <label for="oidc-type" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.type') }}</label>
    <select id="oidc-type" :value="oidc.preset" :class="inputClass" aria-describedby="oidc-type-hint" @change="selectPreset">
      <option v-for="p in presets" :key="p.id" :value="p.id">{{ presetLabel(p) }}</option>
    </select>
    <p id="oidc-type-hint" class="text-xs text-muted mt-1">{{ t(preset.hint) }}</p>
  </div>
  <div v-if="oidc.preset === 'dex'">
    <label for="oidc-dex_connector_type" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.dexConnectorType') }}</label>
    <select id="oidc-dex_connector_type" :value="oidc.dex_connector_type" :class="inputClass" @change="selectConnectorType">
      <option v-for="type in DEX_CONNECTOR_TYPES" :key="type" :value="type">{{ dexConnectorLabel(type) }}</option>
    </select>
  </div>
  <div v-if="oidc.type === ''">
    <label for="issuer" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.issuer') }}</label>
    <input
      id="issuer"
      v-model="oidc.issuer"
      type="url"
      required
      :class="inputClass"
      :placeholder="preset.issuerPattern"
    />
  </div>
  <div>
    <label for="client-id" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.clientId') }}</label>
    <input id="client-id" v-model="oidc.client_id" type="text" required :class="inputClass" />
  </div>
  <div>
    <label for="client-secret" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.clientSecret') }}</label>
    <input
      id="client-secret"
      v-model="oidc.client_secret"
      type="password"
      autocomplete="new-password"
      :required="!editing"
      :class="inputClass"
      placeholder="••••••••••••••••"
    />
    <p v-if="editing" class="text-xs text-muted mt-1">{{ t('adm.sso.field.secretEditHint') }}</p>
  </div>
  <div v-for="field in extraFields" :key="field.key">
    <label :for="`oidc-${field.key}`" class="block text-sm font-medium text-foreground mb-2">{{ field.label }}</label>
    <input
      :id="`oidc-${field.key}`"
      v-model="oidc[field.key]"
      :type="field.url ? 'url' : 'text'"
      :required="field.required"
      :class="inputClass"
    />
    <p v-if="field.hint" class="text-xs text-muted mt-1">{{ field.hint }}</p>
  </div>
  <div class="flex items-center gap-3">
    <AppToggle v-model="oidc.require_email_verified" labelledby="sso-emailverified-label" />
    <span id="sso-emailverified-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.requireEmailVerified') }}</span>
  </div>
  <div v-if="preset.graphGroups" class="flex items-center gap-3">
    <AppToggle v-model="oidc.graph_groups" labelledby="sso-graphgroups-label" />
    <span id="sso-graphgroups-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.graphGroups') }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AppToggle from '../../../components/AppToggle.vue'
import type { SSOProvider } from '../../../api/sso'
import { t } from '../../../i18n'
import type { OIDCTextKey } from '../../../lib/ssoOidcForm'
import {
  DEX_CONNECTOR_TYPES, SSO_PRESETS, SSO_PRESET_IDS, applyDexConnectorType, applyPreset, dexConnectorLabel, presetLabel,
  type DexConnectorType, type SSOPresetId,
} from '../../../lib/ssoPresets'
import type { SSOFormState } from '../../../lib/ssoProviderForm'

// The OIDC settings: the vendor preset decides which of them are shown.
defineProps<{ editing: SSOProvider | null }>()
const form = defineModel<SSOFormState>({ required: true })

interface ExtraField {
  key: OIDCTextKey
  label: string
  hint?: string
  url?: boolean
  required?: boolean
}

const presets = SSO_PRESET_IDS.map((id) => SSO_PRESETS[id])
const oidc = computed(() => form.value.oidc_config)
const preset = computed(() => SSO_PRESETS[oidc.value.preset])

function field(key: OIDCTextKey): ExtraField {
  switch (key) {
    case 'scopes':
      return { key, label: t('adm.sso.field.scopes'), hint: oidc.value.preset === 'dex' ? t('adm.sso.field.dexScopesHint') : t('adm.sso.field.scopesHint') }
    case 'allowed_orgs':
      return { key, label: t('adm.sso.field.allowedOrgs'), hint: t('adm.sso.field.allowedOrgsHint'), required: true }
    case 'auth_base_url':
      return { key, label: t('adm.sso.field.authBaseUrl'), hint: t('adm.sso.field.authBaseUrlHint'), url: true }
    case 'api_base_url':
      return { key, label: t('adm.sso.field.apiBaseUrl'), hint: t('adm.sso.field.apiBaseUrlHint'), url: true }
    case 'authorization_url':
      return { key, label: t('adm.sso.field.authorizationUrl'), url: true, required: true }
    case 'token_url':
      return { key, label: t('adm.sso.field.tokenUrl'), url: true, required: true }
    case 'userinfo_url':
      return { key, label: t('adm.sso.field.userinfoUrl'), url: true, required: true }
    case 'dex_connector_id':
      return { key, label: t('adm.sso.field.dexConnectorId'), hint: t('adm.sso.field.dexConnectorIdHint'), required: true }
    case 'allowed_tenants':
      return { key, label: t('adm.sso.field.allowedTenants'), hint: t('adm.sso.field.allowedTenantsHint') }
    case 'hosted_domain':
      return { key, label: t('adm.sso.field.hostedDomain'), hint: t('adm.sso.field.hostedDomainHint') }
    case 'graph_endpoint':
      return { key, label: t('adm.sso.field.graphEndpoint'), hint: t('adm.sso.field.graphEndpointHint'), url: true }
  }
}

// The scopes come first, except behind Dex where the connector ID matters more.
const extraFields = computed<ExtraField[]>(() => {
  const keys: OIDCTextKey[] = oidc.value.preset === 'dex' ? [...preset.value.fields, 'scopes'] : ['scopes', ...preset.value.fields]
  return keys.map(field)
})

function selectPreset(event: Event): void {
  applyPreset(form.value, (event.target as HTMLSelectElement).value as SSOPresetId)
}

function selectConnectorType(event: Event): void {
  applyDexConnectorType(form.value, (event.target as HTMLSelectElement).value as DexConnectorType)
}

const inputClass =
  'w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary'
</script>
