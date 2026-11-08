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

    <SSOPresetFields v-if="form.kind === 'oidc'" v-model="form" :editing="editing" />

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

    <SSOIconPicker v-model="form.icon" :resolved="editing?.resolved_icon" />

    <SSOCallbackUrl :url="callbackUrl" :kind="form.kind" :exact="!!editing?.callback_url" />

    <SSODexSnippet v-if="form.kind === 'oidc' && form.oidc_config.preset === 'dex'" :form="form" :callback-url="callbackUrl" />

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
import { ssoCallbackUrl } from '../../../lib/ssoPresets'
import type { SSOFormState } from '../../../lib/ssoProviderForm'
import { t } from '../../../i18n'
import SSOCallbackUrl from './SSOCallbackUrl.vue'
import SSODexSnippet from './SSODexSnippet.vue'
import SSOIconPicker from './SSOIconPicker.vue'
import SSOPresetFields from './SSOPresetFields.vue'

const props = defineProps<{ editing: SSOProvider | null }>()
const emit = defineEmits<{ submit: [] }>()
const form = defineModel<SSOFormState>({ required: true })

// A saved provider shows the exact URL the server built from BASE_URL; a new one the URL of the page's origin.
const callbackUrl = computed(
  () => props.editing?.callback_url || ssoCallbackUrl(window.location.origin, form.value.kind, form.value.slug),
)
</script>
