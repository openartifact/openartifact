<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings of a docker repository: what its image signatures and attestations are verified against. -->
<template>
  <section class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('repos.trust.title')" data-testid="attestation-trust">
    <div>
      <h2 class="text-sm font-semibold text-foreground">{{ t('repos.trust.title') }}</h2>
      <p class="mt-1 max-w-2xl text-sm text-muted">{{ t('repos.trust.hint') }}</p>
      <p class="mt-1 max-w-2xl text-xs text-muted">{{ t('repos.trust.keylessHint') }}</p>
    </div>

    <p v-if="loading" role="status" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <StateBlock v-else-if="loadError" variant="error" :title="t('repos.trust.loadFailed')" :hint="loadError">
      <template #action><AppButton variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton></template>
    </StateBlock>

    <form v-else class="space-y-5" novalidate @submit.prevent="save">
      <fieldset class="space-y-2">
        <legend class="text-sm font-medium text-foreground">{{ t('repos.trust.keys') }}</legend>
        <p class="text-xs text-muted">{{ t('repos.trust.keys.hint') }}</p>
        <div v-for="(_, i) in form.keys" :key="i" class="flex items-start gap-2">
          <textarea v-model="form.keys[i]" rows="4" spellcheck="false" :aria-label="tp('repos.trust.keys.entry', { n: i + 1 })" :class="MONO_INPUT_CLASS" data-testid="trust-key" />
          <AppButton variant="secondary" size="sm" @click="form.keys.splice(i, 1)">{{ t('repos.trust.remove') }}</AppButton>
        </div>
        <AppButton variant="secondary" size="sm" data-testid="trust-add-key" @click="form.keys.push('')">{{ t('repos.trust.keys.add') }}</AppButton>
      </fieldset>

      <fieldset class="space-y-2">
        <legend class="text-sm font-medium text-foreground">{{ t('repos.trust.roots') }}</legend>
        <p class="text-xs text-muted">{{ t('repos.trust.roots.hint') }}</p>
        <div v-for="(_, i) in form.roots" :key="i" class="flex items-start gap-2">
          <textarea v-model="form.roots[i]" rows="4" spellcheck="false" :aria-label="tp('repos.trust.roots.entry', { n: i + 1 })" :class="MONO_INPUT_CLASS" data-testid="trust-root" />
          <AppButton variant="secondary" size="sm" @click="form.roots.splice(i, 1)">{{ t('repos.trust.remove') }}</AppButton>
        </div>
        <AppButton variant="secondary" size="sm" data-testid="trust-add-root" @click="form.roots.push('')">{{ t('repos.trust.roots.add') }}</AppButton>
      </fieldset>

      <fieldset class="space-y-2">
        <legend class="text-sm font-medium text-foreground">{{ t('repos.trust.identities') }}</legend>
        <p class="text-xs text-muted">{{ t('repos.trust.identities.hint') }}</p>
        <div v-for="(identity, i) in form.identities" :key="i" class="grid items-end gap-2 md:grid-cols-[minmax(0,1fr)_9rem_minmax(0,1fr)_auto]" data-testid="trust-identity">
          <FormField :label="t('repos.trust.issuer')" :id="`trust-issuer-${i}`">
            <input :id="`trust-issuer-${i}`" v-model="identity.issuer" type="url" placeholder="https://token.actions.githubusercontent.com" :class="INPUT_CLASS" />
          </FormField>
          <FormField :label="t('repos.trust.match')" :id="`trust-match-${i}`">
            <select :id="`trust-match-${i}`" v-model="identity.match" :class="INPUT_CLASS">
              <option value="subject">{{ t('repos.trust.match.subject') }}</option>
              <option value="regexp">{{ t('repos.trust.match.regexp') }}</option>
            </select>
          </FormField>
          <FormField :label="identity.match === 'regexp' ? t('repos.trust.match.regexp') : t('repos.trust.match.subject')" :id="`trust-subject-${i}`">
            <input :id="`trust-subject-${i}`" v-model="identity.value" type="text" spellcheck="false" :class="MONO_INPUT_CLASS" />
          </FormField>
          <AppButton variant="secondary" size="sm" class="mb-1" @click="form.identities.splice(i, 1)">{{ t('repos.trust.remove') }}</AppButton>
        </div>
        <AppButton variant="secondary" size="sm" data-testid="trust-add-identity" @click="form.identities.push({ issuer: '', match: 'subject', value: '' })">
          {{ t('repos.trust.identities.add') }}
        </AppButton>
      </fieldset>

      <div class="flex items-start gap-3">
        <AppToggle v-model="form.requireProvenance" labelledby="trust-require-label" />
        <div>
          <p id="trust-require-label" class="text-sm font-medium text-foreground">{{ t('repos.trust.require') }}</p>
          <p class="text-xs text-muted">{{ t('repos.trust.require.hint') }}</p>
        </div>
      </div>

      <p v-if="saveError" role="alert" class="text-sm text-danger" data-testid="trust-error">{{ saveError }}</p>
      <div class="flex flex-wrap justify-end gap-2">
        <AppButton variant="secondary" :loading="reverifying" data-testid="trust-reverify" @click="reverify">{{ t('repos.trust.reverify') }}</AppButton>
        <AppButton type="submit" :disabled="!dirty" :loading="saving">{{ saving ? t('common.saving') : t('common.save') }}</AppButton>
      </div>
    </form>
  </section>
</template>

<script setup lang="ts">
import AppButton from '../AppButton.vue'
import AppToggle from '../AppToggle.vue'
import FormField from '../FormField.vue'
import StateBlock from '../ui/StateBlock.vue'
import { useAttestationTrust } from '../../composables/useAttestationTrust'
import { INPUT_CLASS, MONO_INPUT_CLASS } from '../../lib/formClasses'
import { t, tp } from '../../i18n'

const props = defineProps<{ repo: string }>()

const { form, dirty, loading, loadError, saving, saveError, reverifying, load, save, reverify } = useAttestationTrust(() => props.repo)
</script>
