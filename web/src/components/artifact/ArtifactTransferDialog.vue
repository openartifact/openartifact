<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  "Copy to..." and "Move to...": pick a hosted repository of the same format the caller
  can write, read the dry-run report of what would happen, then confirm. A move takes the
  version out of this repository into its trash.
-->
<template>
  <DialogFrame :open="open" panel-class="w-full max-w-lg rounded-xl border p-6 shadow-black/30" @close="close">
    <DialogTitle as="h2" class="text-base font-semibold text-foreground">{{ title }}</DialogTitle>
    <DialogDescription class="mt-1 text-sm text-muted">{{ description }}</DialogDescription>

    <div class="mt-4 space-y-4 text-sm">
      <p v-if="loadingTargets" role="status" class="text-muted">{{ t('common.loading') }}</p>
      <p v-else-if="targets.length === 0" class="text-muted" data-testid="transfer-no-targets">{{ t('artifact.transfer.noTargets') }}</p>
      <label v-else class="block">
        <span class="text-foreground">{{ t('artifact.transfer.target') }}</span>
        <select
          v-model="target"
          class="mt-1 w-full rounded-md border border-border-strong bg-bg px-3 py-2 font-mono text-sm text-foreground focus:ring-2 focus:ring-primary"
          data-testid="transfer-target"
        >
          <option value="" disabled>{{ t('artifact.transfer.choose') }}</option>
          <option v-for="name in targets" :key="name" :value="name">{{ name }}</option>
        </select>
      </label>

      <p v-if="previewing" role="status" class="text-muted">{{ t('artifact.transfer.previewing') }}</p>
      <p v-if="error" role="alert" class="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-danger" data-testid="transfer-error">{{ error }}</p>

      <div v-if="preview" class="rounded-md border border-border bg-surface-hover px-3 py-2" data-testid="transfer-preview">
        <p class="font-medium text-foreground">{{ t('artifact.transfer.preview') }}</p>
        <ul class="mt-1 list-disc pl-5 text-muted">
          <li>{{ tpn('artifact.transfer.willCopy', preview.copied ?? 0) }}</li>
          <li v-if="(preview.existing ?? 0) > 0">{{ tpn('artifact.transfer.existing', preview.existing ?? 0) }}</li>
          <li v-if="mode === 'move'">{{ tp('artifact.transfer.willMove', { repo }) }}</li>
        </ul>
      </div>
    </div>

    <div class="mt-6 flex justify-end gap-2">
      <AppButton variant="secondary" :disabled="busy" @click="close">{{ t('common.cancel') }}</AppButton>
      <AppButton :variant="mode === 'move' ? 'danger' : 'primary'" :loading="busy" :disabled="!preview || busy" data-testid="transfer-confirm" @click="confirm">
        {{ mode === 'move' ? t('artifact.transfer.moveAction') : t('artifact.transfer.copyAction') }}
      </AppButton>
    </div>
  </DialogFrame>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { DialogDescription, DialogTitle } from '@headlessui/vue'
import AppButton from '../AppButton.vue'
import DialogFrame from '../ui/DialogFrame.vue'
import { listTransferTargets, transferArtifacts, type CopyItem, type CopyResult, type TransferOperation } from '../../api/transfer'
import { t, tp, tpn } from '../../i18n'

const props = defineProps<{
  open: boolean
  mode: TransferOperation
  repo: string
  format: string
  isAdmin: boolean
  /** The version being transferred, for the text ("name 1.2.3"). */
  label: string
  items: CopyItem[]
}>()
const emit = defineEmits<{ close: []; done: [target: string] }>()

const targets = ref<string[]>([])
const loadingTargets = ref(false)
const target = ref('')
const preview = ref<CopyResult | null>(null)
const previewing = ref(false)
const error = ref('')
const busy = ref(false)

const title = computed(() => tp(props.mode === 'move' ? 'artifact.transfer.moveTitle' : 'artifact.transfer.copyTitle', { name: props.label }))
const description = computed(() => tp(props.mode === 'move' ? 'artifact.transfer.moveHint' : 'artifact.transfer.copyHint', { repo: props.repo }))

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : t('artifact.transfer.failed')
}

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    target.value = ''
    preview.value = null
    error.value = ''
    loadingTargets.value = true
    try {
      targets.value = await listTransferTargets(props.format, props.repo, props.isAdmin)
    } catch (err) {
      targets.value = []
      error.value = errorText(err)
    } finally {
      loadingTargets.value = false
    }
  },
  { immediate: true },
)

// Choosing a target runs the dry run: nothing is written until the confirm.
watch(target, async (name) => {
  preview.value = null
  error.value = ''
  if (!name) return
  previewing.value = true
  try {
    const result = await transferArtifacts(props.mode, props.repo, name, props.items, true)
    if (target.value === name) preview.value = result
  } catch (err) {
    if (target.value === name) error.value = errorText(err)
  } finally {
    previewing.value = false
  }
})

async function confirm(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    await transferArtifacts(props.mode, props.repo, target.value, props.items, false)
    emit('done', target.value)
  } catch (err) {
    error.value = errorText(err)
  } finally {
    busy.value = false
  }
}

function close(): void {
  if (!busy.value) emit('close')
}
</script>
