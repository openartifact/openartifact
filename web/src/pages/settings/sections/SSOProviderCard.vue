<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div
    class="bg-surface rounded-xl border border-border p-5 relative overflow-hidden group"
  >
    <div
      class="absolute left-0 top-0 bottom-0 w-[3px]"
      :class="provider.enabled ? 'bg-success' : 'bg-muted'"
    />
    <div class="pl-2">
      <div class="flex items-start justify-between mb-3">
        <h3 class="font-semibold text-foreground">{{ provider.display_name }}</h3>
        <span class="font-mono text-micro px-1.5 py-0.5 rounded bg-surface-hover text-muted">
          {{ provider.kind.toUpperCase() }}
        </span>
      </div>

      <p class="text-caption font-mono text-muted mb-3">{{ provider.slug }}</p>

      <div class="flex items-center justify-between mb-4">
        <span
          class="inline-flex items-center px-2 py-1 rounded-full text-xs font-medium"
          :class="provider.enabled ? 'bg-success/20 text-success' : 'bg-muted/20 text-muted'"
        >
          {{ provider.enabled ? t('adm.sso.enabled') : t('adm.sso.disabled') }}
        </span>
        <p class="text-caption text-muted" :class="testResult?.ok === false ? 'text-danger' : ''">
          <template v-if="testResult">
            {{ testResult.ok ? t('adm.sso.testOkAt') : t('adm.sso.testFailedAt') }}
            {{ testResult.at }}
          </template>
          <template v-else>{{ t('adm.sso.updated') }} {{ timeAgo(provider.updated_at, { maxDays: 7 }) }}</template>
        </p>
      </div>

      <p
        v-if="testResult?.ok === false && testResult.message"
        class="mb-3 text-xs text-danger break-words"
        role="alert"
      >
        {{ testResult.message }}
      </p>

      <div class="flex items-center gap-2">
        <AppButton variant="secondary" size="xs" :loading="testing" @click="emit('test')">
          <BoltIcon class="w-3.5 h-3.5" aria-hidden="true" />
          {{ t('adm.sso.test') }}
        </AppButton>
        <AppButton variant="secondary" size="xs" @click="emit('edit')">
          <PencilIcon class="w-3.5 h-3.5" aria-hidden="true" />
          {{ t('common.edit') }}
        </AppButton>
        <AppToggle
          class="ml-auto"
          size="sm"
          :model-value="provider.enabled"
          :label="`${provider.enabled ? t('adm.sso.disable') : t('adm.sso.enable')}: ${provider.display_name}`"
          @update:model-value="emit('toggle')"
        />
        <AppButton
          variant="danger-outline"
          size="xs"
          :title="t('common.delete')"
          :aria-label="`${t('common.delete')}: ${provider.display_name}`"
          @click="emit('delete')"
        >
          <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
        </AppButton>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { BoltIcon, PencilIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AppButton from '../../../components/AppButton.vue'
import AppToggle from '../../../components/AppToggle.vue'
import type { SSOProvider } from '../../../api/sso'
import { t } from '../../../i18n'
import { timeAgo } from '../../../lib/datetime'
import type { SSOTestOutcome } from '../../../lib/ssoProviderForm'

defineProps<{ provider: SSOProvider; testResult?: SSOTestOutcome; testing: boolean }>()
const emit = defineEmits<{ test: []; edit: []; toggle: []; delete: [] }>()
</script>
