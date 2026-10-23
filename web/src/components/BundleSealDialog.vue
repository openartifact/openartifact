<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ConfirmDialog
    :open="open"
    :title="t('bundles.seal.title')"
    :description="t('bundles.seal.description')"
    :confirm-label="t('bundles.action.seal')"
    tone="primary"
    :busy="busy"
    @confirm="seal"
    @close="emit('close')"
  >
    <FormField :label="t('bundles.seal.key')" id="seal-key">
      <select id="seal-key" v-model="keyId" :class="INPUT_CLASS">
        <option value="">{{ t('bundles.seal.noKey') }}</option>
        <option v-for="k in keys" :key="keyValue(k)" :value="keyValue(k)">{{ keyLabel(k) }}</option>
      </select>
    </FormField>
    <p v-if="keysFailed" class="mt-2 text-xs text-warning" role="status">{{ t('bundles.seal.keysFailed') }}</p>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import ConfirmDialog from './ConfirmDialog.vue'
import FormField from './FormField.vue'
import { sealBundle, type Bundle } from '../api/bundles'
import { listGpgKeys, type GpgKey } from '../api/gpg'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ open: boolean; name: string; version: string }>()
const emit = defineEmits<{ close: []; sealed: [bundle: Bundle] }>()

const toasts = useToastStore()
const keys = ref<GpgKey[]>([])
const keyId = ref('')
const keysFailed = ref(false)
const busy = ref(false)

/** The value the seal API accepts: the key id from the admin list (`config` for the configured key). */
function keyValue(k: GpgKey): string {
  return k.id ?? k.fingerprint ?? ''
}

function keyLabel(k: GpgKey): string {
  const id = k.key_id ?? (k.fingerprint ?? '').slice(-16)
  const label = k.name || k.user_id || ''
  return label ? `${label} (${id})` : id
}

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    keyId.value = ''
    keysFailed.value = false
    try {
      keys.value = (await listGpgKeys()).keys
    } catch {
      keys.value = []
      keysFailed.value = true
    }
  },
  { immediate: true },
)

async function seal(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    const bundle = await sealBundle(props.name, props.version, keyId.value || undefined)
    toasts.success(t('bundles.seal.done'))
    emit('sealed', bundle)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.seal.failed'))
  } finally {
    busy.value = false
  }
}
</script>
