<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="open"
    drawer
    :eyebrow="mode === 'create' ? t('adm.storage.newBackend') : t('adm.storage.editBackend')"
    :title="mode === 'create' ? t('adm.storage.addTitle') : tp('adm.storage.editTitle', { name: backend?.name ?? '' })"
    @close="emit('close')"
  >
    <div class="space-y-6">
      <!-- Name -->
      <div>
        <label class="block text-xs font-semibold text-foreground mb-1.5">
          {{ t('adm.storage.field.name') }} <span class="text-danger ml-0.5">*</span>
        </label>
        <input
          v-model="form.name"
          type="text"
          :disabled="mode === 'edit'"
          :placeholder="t('adm.storage.field.namePlaceholder')"
          class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary disabled:opacity-60 disabled:cursor-not-allowed transition-colors"
        />
        <p v-if="mode === 'edit'" class="text-caption text-muted mt-1 font-mono">
          {{ t('adm.storage.field.nameLocked') }}
        </p>
      </div>

      <!-- Backend type picker -->
      <div>
        <div class="flex items-baseline gap-2 mb-2">
          <label class="text-xs font-semibold text-foreground">
            {{ t('adm.storage.field.backendType') }} <span class="text-danger ml-0.5">*</span>
          </label>
          <span v-if="mode === 'edit'" class="text-caption text-muted font-mono">
            {{ t('adm.storage.field.typeLocked') }}
          </span>
        </div>
        <div class="grid grid-cols-2 gap-2">
          <button
            v-for="(bt, key) in BACKEND_TYPES"
            :key="key"
            type="button"
            :disabled="mode === 'edit'"
            class="relative text-left p-3 rounded-lg border overflow-hidden transition-all focus-visible:ring-2 focus-visible:ring-primary"
            :class="[
              form.kind === key
                ? 'border-primary bg-primary-soft/40 ring-1 ring-primary/30'
                : 'border-border bg-surface hover:bg-surface-hover',
              mode === 'edit' ? 'cursor-default opacity-70' : 'cursor-pointer',
            ]"
            @click="mode === 'create' && selectType(key)"
          >
            <!-- Accent bar -->
            <span
              class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-lg"
              :style="{ background: typeColor(String(key)) }"
            />
            <div class="pl-3 flex items-start gap-2.5">
              <div
                class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 mt-0.5"
                :style="{ background: tint(typeColor(String(key)), 12) }"
              >
                <component :is="typeIcon(key)" class="w-4 h-4" :style="{ color: typeColor(String(key)) }" />
              </div>
              <div class="min-w-0">
                <p class="text-xs font-semibold text-foreground leading-tight">{{ bt.label }}</p>
                <p class="text-caption text-muted leading-snug mt-0.5">{{ bt.desc }}</p>
              </div>
            </div>
          </button>
        </div>
      </div>

      <!-- Dynamic config fields -->
      <div v-if="form.kind && BACKEND_TYPES[form.kind]">
        <label class="block text-xs font-semibold text-foreground mb-3">{{ t('storage.configuration') }}</label>
        <div class="grid grid-cols-2 gap-x-3 gap-y-4">
          <StorageBackendField
            v-for="field in fields"
            :key="field.key"
            :field="field"
            :config="form.config"
            :revealed="!!revealedSecrets[field.key]"
            @update="form.config[field.key] = $event"
            @toggle-reveal="revealedSecrets[field.key] = !revealedSecrets[field.key]"
          />
        </div>
      </div>

      <!-- Set as default toggle -->
      <div class="flex items-start gap-3 p-3.5 rounded-lg border border-border bg-surface-hover/30">
        <AppToggle v-model="form.is_default" size="sm" :label="t('storage.setDefault')" class="mt-0.5" />
        <div>
          <p class="text-xs font-semibold text-foreground">{{ t('storage.setDefault') }}</p>
          <p class="text-caption text-muted mt-0.5 leading-relaxed">
            {{ t('storage.setDefaultHint') }}
            {{ t('adm.storage.onlyOneDefault') }}
          </p>
        </div>
      </div>

      <!-- Test result alert -->
      <div
        v-if="testResult !== null"
        class="rounded-lg border p-3 flex items-start gap-2.5"
        :class="testResult.ok
          ? 'border-success/40 bg-success-soft text-success'
          : 'border-danger/40 bg-danger-soft text-danger'"
      >
        <CheckIcon v-if="testResult.ok" class="w-4 h-4 mt-0.5 shrink-0" />
        <ExclamationCircleIcon v-else class="w-4 h-4 mt-0.5 shrink-0" />
        <div class="min-w-0">
          <p class="text-xs font-semibold">
            {{ testResult.ok ? t('adm.storage.testOk') : t('adm.storage.testFailed') }}
          </p>
          <p v-if="testResult.message" class="text-caption mt-0.5 font-mono break-all">
            {{ testResult.message }}
          </p>
        </div>
      </div>
    </div>

    <template #footer>
      <AppButton
        variant="secondary"
        :loading="testing"
        :disabled="mode === 'create' || !form.name"
        class="mr-auto"
        :title="mode === 'create' ? t('adm.storage.saveFirst') : t('adm.storage.testSaved')"
        @click="test"
      >
        <BoltIcon v-if="!testing" class="w-4 h-4" aria-hidden="true" />
        {{ t('adm.storage.testConnection') }}
      </AppButton>
      <AppButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton :loading="saving" :disabled="!form.name || !form.kind" @click="save">
        {{ mode === 'create' ? t('adm.storage.createAction') : t('adm.storage.saveAction') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { BoltIcon, CheckIcon, ExclamationCircleIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import StorageBackendField from './StorageBackendField.vue'
import { useStorageBackendForm, type DrawerMode } from '../composables/useStorageBackendForm'
import { t, tp } from '../i18n/en'
import { tint } from '../lib/brandColors'
import { BACKEND_TYPES, typeColor, typeIcon, type StorageBackend } from '../lib/storageBackends'

const props = defineProps<{
  open: boolean
  mode: DrawerMode
  /** The backend being edited; null while creating. */
  backend: StorageBackend | null
  /** Refreshes the backend list after a save or connection test. */
  reload: () => Promise<void>
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const { form, fields, revealedSecrets, testResult, testing, saving, init, selectType, test, save } =
  useStorageBackendForm({
    mode: () => props.mode,
    backend: () => props.backend,
    close: () => emit('close'),
    reload: () => props.reload(),
  })

// A freshly opened drawer always starts from a clean (or prefilled) form.
watch(
  () => props.open,
  (open) => {
    if (open) init()
  },
)
</script>
