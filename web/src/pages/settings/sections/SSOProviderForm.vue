<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <form id="sso-form" class="space-y-5" @submit.prevent="emit('submit')">
    <div>
      <label for="display-name" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.displayName') }}</label>
      <input
        id="display-name"
        v-model="form.display_name"
        type="text"
        required
        class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary"
        placeholder="Google Workspace"
      />
    </div>

    <div>
      <label for="slug" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.slug') }}</label>
      <input
        id="slug"
        v-model="form.slug"
        type="text"
        required
        pattern="[a-z0-9_-]{1,64}"
        :disabled="!!editing"
        class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary disabled:opacity-50 disabled:cursor-not-allowed"
        placeholder="google-workspace"
      />
      <p class="text-xs text-muted mt-1">{{ t('adm.sso.field.slugHint') }}</p>
    </div>

    <fieldset>
      <legend class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.kind') }}</legend>
      <div class="flex gap-2" role="radiogroup">
        <button
          v-for="kind in (['oidc', 'saml'] as const)"
          :key="kind"
          type="button"
          role="radio"
          :aria-checked="form.kind === kind"
          @click="form.kind = kind"
          :disabled="!!editing"
          class="flex-1 px-3 py-2 rounded-lg text-sm font-medium transition-colors disabled:cursor-not-allowed border"
          :class="form.kind === kind
            ? 'bg-primary text-primary-contrast border-primary'
            : 'bg-surface-hover text-foreground border-border-strong hover:bg-border disabled:opacity-50'"
        >
          {{ kind.toUpperCase() }}
        </button>
      </div>
    </fieldset>

    <template v-if="form.kind === 'oidc'">
      <div>
        <label for="oidc-type" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.type') }}</label>
        <select id="oidc-type" v-model="form.oidc_config.type" :class="inputClass">
          <option value="">{{ t('adm.sso.type.oidc') }}</option>
          <option value="github">{{ t('adm.sso.type.github') }}</option>
          <option value="oauth2">{{ t('adm.sso.type.oauth2') }}</option>
        </select>
      </div>
      <div v-if="form.oidc_config.type === ''">
        <label for="issuer" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.issuer') }}</label>
        <input
          id="issuer"
          v-model="form.oidc_config.issuer"
          type="url"
          required
          :class="inputClass"
          placeholder="https://accounts.google.com"
        />
      </div>
      <div>
        <label for="client-id" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.clientId') }}</label>
        <input
          id="client-id"
          v-model="form.oidc_config.client_id"
          type="text"
          required
          :class="inputClass"
        />
      </div>
      <div>
        <label for="client-secret" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.clientSecret') }}</label>
        <input
          id="client-secret"
          v-model="form.oidc_config.client_secret"
          type="password"
          autocomplete="new-password"
          :required="!editing"
          :class="inputClass"
          placeholder="••••••••••••••••"
        />
        <p v-if="editing" class="text-xs text-muted mt-1">{{ t('adm.sso.field.secretEditHint') }}</p>
      </div>
      <div v-for="field in oidcExtraFields" :key="field.key">
        <label :for="`oidc-${field.key}`" class="block text-sm font-medium text-foreground mb-2">{{ field.label }}</label>
        <input
          :id="`oidc-${field.key}`"
          v-model="form.oidc_config[field.key]"
          :type="field.url ? 'url' : 'text'"
          :required="field.required"
          :class="inputClass"
        />
        <p v-if="field.hint" class="text-xs text-muted mt-1">{{ field.hint }}</p>
      </div>
      <div class="flex items-center gap-3">
        <AppToggle v-model="form.oidc_config.require_email_verified" labelledby="sso-emailverified-label" />
        <span id="sso-emailverified-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.requireEmailVerified') }}</span>
      </div>
      <div v-if="form.oidc_config.type === ''" class="flex items-center gap-3">
        <AppToggle v-model="form.oidc_config.graph_groups" labelledby="sso-graphgroups-label" />
        <span id="sso-graphgroups-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.graphGroups') }}</span>
      </div>
    </template>

    <template v-else>
      <div>
        <label for="idp-metadata-url" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.metadataUrl') }}</label>
        <input
          id="idp-metadata-url"
          v-model="form.saml_config.idp_metadata_url"
          type="url"
          class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary"
          placeholder="https://idp.example.com/metadata.xml"
        />
      </div>
      <div>
        <label for="idp-metadata-xml" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.metadataXml') }}</label>
        <textarea
          id="idp-metadata-xml"
          v-model="form.saml_config.idp_metadata_xml"
          rows="5"
          class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary resize-none"
        />
      </div>
    </template>

    <div class="flex items-center gap-3">
      <AppToggle v-model="form.auto_provision_users" labelledby="sso-autoprovision-label" />
      <span id="sso-autoprovision-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.autoProvision') }}</span>
    </div>

    <div class="flex items-center gap-3">
      <AppToggle v-model="form.link_by_email" labelledby="sso-linkemail-label" />
      <span id="sso-linkemail-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.linkByEmail') }}</span>
    </div>
  </form>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AppToggle from '../../../components/AppToggle.vue'
import type { SSOProvider } from '../../../api/sso'
import type { OIDCTextKey } from '../../../lib/ssoOidcForm'
import type { SSOFormState } from '../../../lib/ssoProviderForm'
import { t } from '../../../i18n'

defineProps<{ editing: SSOProvider | null }>()
const emit = defineEmits<{ submit: [] }>()
const form = defineModel<SSOFormState>({ required: true })

interface ExtraField {
  key: OIDCTextKey
  label: string
  hint?: string
  url?: boolean
  required?: boolean
}

// The settings that apply to the selected provider type, after the common ones.
const oidcExtraFields = computed<ExtraField[]>(() => {
  const scopes: ExtraField = { key: 'scopes', label: t('adm.sso.field.scopes'), hint: t('adm.sso.field.scopesHint') }
  switch (form.value.oidc_config.type) {
    case 'github':
      return [
        scopes,
        { key: 'allowed_orgs', label: t('adm.sso.field.allowedOrgs'), hint: t('adm.sso.field.allowedOrgsHint'), required: true },
        { key: 'auth_base_url', label: t('adm.sso.field.authBaseUrl'), hint: t('adm.sso.field.authBaseUrlHint'), url: true },
        { key: 'api_base_url', label: t('adm.sso.field.apiBaseUrl'), hint: t('adm.sso.field.apiBaseUrlHint'), url: true },
      ]
    case 'oauth2':
      return [
        scopes,
        { key: 'authorization_url', label: t('adm.sso.field.authorizationUrl'), url: true, required: true },
        { key: 'token_url', label: t('adm.sso.field.tokenUrl'), url: true, required: true },
        { key: 'userinfo_url', label: t('adm.sso.field.userinfoUrl'), url: true, required: true },
      ]
    default:
      return [
        scopes,
        { key: 'allowed_tenants', label: t('adm.sso.field.allowedTenants'), hint: t('adm.sso.field.allowedTenantsHint') },
        { key: 'hosted_domain', label: t('adm.sso.field.hostedDomain'), hint: t('adm.sso.field.hostedDomainHint') },
        { key: 'graph_endpoint', label: t('adm.sso.field.graphEndpoint'), hint: t('adm.sso.field.graphEndpointHint'), url: true },
      ]
  }
})

const inputClass =
  'w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary'

</script>
