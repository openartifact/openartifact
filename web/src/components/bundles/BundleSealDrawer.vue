<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Seal and sign: the one step that freezes a draft. Sealing computes the manifest and, with a key
  chosen, signs it in the same call. It cannot be undone, so the drawer says what it locks.
-->
<template>
  <Drawer :open="open" :title="tp('bundles.seal.title', { bundle: `${name} ${version}` })" :eyebrow="t('bundles.nav')" :busy="busy" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-foreground">{{ tp('bundles.seal.summary', { count: String(itemCount) }) }}</p>

      <FormField v-if="keys.length > 0" :label="t('bundles.seal.key')" id="seal-key" :hint="keyHint">
        <select id="seal-key" v-model="keyId" :disabled="loadingKeys" :class="INPUT_CLASS">
          <option v-for="k in keys" :key="k.id" :value="k.id">{{ keyLabel(k) }}</option>
          <option value="">{{ t('bundles.seal.noKey') }}</option>
        </select>
      </FormField>
      <p v-if="keysFailed" class="text-xs text-warning" role="status">{{ t('bundles.seal.keysFailed') }}</p>
      <p v-else-if="!loadingKeys && keys.length === 0" class="text-sm text-muted" data-testid="seal-no-keys">{{ t('bundles.seal.noKeys') }}</p>

      <p class="text-sm text-muted">{{ t('bundles.seal.irreversible') }}</p>
      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    </div>

    <template #footer>
      <AppButton variant="secondary" size="sm" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" :loading="busy" :disabled="loadingKeys" @click="seal">
        {{ keyId ? t('bundles.action.seal') : t('bundles.action.sealOnly') }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import { ApiError } from '../../api/client'
import { listBundleSigningKeys, sealBundle, type Bundle, type BundleSigningKey } from '../../api/bundles'
import { INPUT_CLASS } from '../../lib/formClasses'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const props = defineProps<{ open: boolean; name: string; version: string; itemCount: number }>()
const emit = defineEmits<{ close: []; sealed: [bundle: Bundle] }>()

const toasts = useToastStore()
const keys = ref<BundleSigningKey[]>([])
const keyId = ref('')
const loadingKeys = ref(false)
const keysFailed = ref(false)
const busy = ref(false)
const error = ref('')

function keyLabel(k: BundleSigningKey): string {
  const id = k.key_id || k.fingerprint.slice(-16)
  const label = k.name || k.user_id
  return label ? `${label} (${id})` : id
}

const keyHint = computed(() => (keyId.value ? t('bundles.seal.keyHint') : t('bundles.seal.unsignedHint')))

// The server lists only the keys this user may sign bundles with; with none the bundle can only be
// sealed unsigned. Every opening reloads them and signs with the globally active one when permitted.
async function loadKeys(): Promise<void> {
  keysFailed.value = false
  loadingKeys.value = true
  try {
    keys.value = await listBundleSigningKeys()
    const preferred = keys.value.find((k) => k.active_global) ?? keys.value[0]
    keyId.value = preferred?.id ?? ''
  } catch {
    keys.value = []
    keyId.value = ''
    keysFailed.value = true
  } finally {
    loadingKeys.value = false
  }
}

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    error.value = ''
    await loadKeys()
  },
  { immediate: true },
)

/** A 403 `signer_forbidden` means the grant changed since the keys were listed: say so and refresh them. */
async function sealFailure(err: unknown): Promise<string> {
  if (err instanceof ApiError && err.status === 403 && err.code === 'signer_forbidden') {
    await loadKeys()
    return t('bundles.seal.signerForbidden')
  }
  return err instanceof Error ? err.message : t('bundles.seal.failed')
}

async function seal(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const bundle = await sealBundle(props.name, props.version, keyId.value || undefined)
    toasts.success(keyId.value ? t('bundles.seal.doneSigned') : t('bundles.seal.done'), tp('bundles.seal.doneHint', { bundle: `${props.name} ${props.version}` }))
    emit('sealed', bundle)
  } catch (err) {
    error.value = await sealFailure(err)
  } finally {
    busy.value = false
  }
}
</script>
