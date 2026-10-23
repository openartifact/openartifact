<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog drawer max-width="max-w-xl" :open="open" :eyebrow="t('bundles.items.eyebrow')" :title="t('bundles.action.addItems')" @close="emit('close')">
    <form id="bundle-item-form" class="space-y-5" novalidate @submit.prevent="submit">
      <FormField :label="t('bundles.items.repo')" id="item-repo" :error="errors.source_repo_id">
        <select id="item-repo" v-model="form.source_repo_id" :class="INPUT_CLASS" @change="followRepoFormat">
          <option value="">{{ t('bundles.items.chooseRepo') }}</option>
          <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }} ({{ r.format }})</option>
        </select>
      </FormField>
      <FormField :label="t('bundles.items.sha')" id="item-sha" :error="errors.sha256">
        <input id="item-sha" v-model="form.sha256" type="text" spellcheck="false" autocomplete="off" :class="[INPUT_CLASS, 'font-mono text-xs']" />
      </FormField>
      <FormField :label="t('bundles.items.path')" id="item-path" :error="errors.artifact_path">
        <input id="item-path" v-model="form.artifact_path" type="text" autocomplete="off" :class="[INPUT_CLASS, 'font-mono text-xs']" />
      </FormField>
      <div class="grid grid-cols-2 gap-4">
        <FormField :label="`${t('bundles.items.format')} (${t('common.optional')})`" id="item-format">
          <select id="item-format" v-model="form.format" :class="INPUT_CLASS">
            <option value="">{{ t('bundles.items.formatDefault') }}</option>
            <option v-for="f in formats" :key="f" :value="f">{{ f }}</option>
          </select>
        </FormField>
        <FormField :label="`${t('bundles.items.size')} (${t('common.optional')})`" id="item-size" :error="errors.size_bytes">
          <input id="item-size" v-model="form.size_bytes" type="text" inputmode="numeric" autocomplete="off" :class="INPUT_CLASS" />
        </FormField>
      </div>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="bundle-item-form" :loading="busy">
        {{ t('bundles.action.addItems') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import { addBundleItems, type BundleItem } from '../api/bundles'
import type { Repository } from '../api/repositories'
import { itemFormToInput, validateItemForm, type ItemFormErrors, type ItemFormValues } from '../lib/bundles'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ open: boolean; repos: Repository[]; name: string; version: string }>()
const emit = defineEmits<{ close: []; added: [items: BundleItem[]] }>()

const toasts = useToastStore()
const blank = (): ItemFormValues => ({ source_repo_id: '', sha256: '', artifact_path: '', format: '', size_bytes: '' })
const form = ref<ItemFormValues>(blank())
const errors = ref<ItemFormErrors>({})
const busy = ref(false)

const formats = computed(() => [...new Set(props.repos.map((r) => r.format))].sort())

watch(
  () => props.open,
  (open) => {
    if (!open) return
    form.value = blank()
    errors.value = {}
  },
)

/** The item format defaults to the repository's; clear an override that no longer applies. */
function followRepoFormat(): void {
  form.value.format = ''
}

async function submit(): Promise<void> {
  if (busy.value) return
  errors.value = validateItemForm(form.value)
  if (Object.keys(errors.value).length > 0) return
  busy.value = true
  try {
    const items = await addBundleItems(props.name, props.version, [itemFormToInput(form.value)])
    toasts.success(t('bundles.items.added'))
    emit('added', items)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.items.addFailed'))
  } finally {
    busy.value = false
  }
}
</script>
