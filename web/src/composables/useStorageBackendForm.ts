// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, reactive, ref } from 'vue'
import { createStorageBackend, testStorageBackend, updateStorageBackend } from '../api/storage'
import { t, tp } from '../i18n/en'
import { submittableConfig, visibleFields, type StorageBackend } from '../lib/storageBackends'
import { useToastStore } from '../stores/toast'

export type DrawerMode = 'create' | 'edit'

interface FormOptions {
  mode: () => DrawerMode
  backend: () => StorageBackend | null
  close: () => void
  /** Refreshes the backend list; awaited so the save button stays busy until it lands. */
  reload: () => Promise<void>
}

/** Form state, connection test and save logic of the storage backend drawer. */
export function useStorageBackendForm(opts: FormOptions) {
  const toasts = useToastStore()
  const saving = ref(false)
  const testing = ref(false)
  const testResult = ref<{ ok: boolean; message?: string } | null>(null)
  const revealedSecrets = reactive<Record<string, boolean>>({})
  const form = reactive<{
    name: string
    kind: string
    is_default: boolean
    config: Record<string, unknown>
  }>({
    name: '',
    kind: '',
    is_default: false,
    config: {},
  })

  const fields = computed(() => visibleFields(form.kind, form.config))

  function reset(): void {
    form.name = ''
    form.kind = ''
    form.is_default = false
    form.config = {}
    testResult.value = null
    Object.keys(revealedSecrets).forEach((k) => {
      delete revealedSecrets[k]
    })
  }

  /** Prepares the form for a freshly opened drawer: blank when creating, prefilled when editing. */
  function init(): void {
    reset()
    const backend = opts.backend()
    if (opts.mode() !== 'edit' || !backend) return
    form.name = backend.name
    form.kind = backend.kind
    form.is_default = backend.is_default
    form.config = { ...backend.config }
  }

  function selectType(kind: string): void {
    form.kind = kind
    form.config = {}
    testResult.value = null
  }

  async function test(): Promise<void> {
    const backend = opts.backend()
    if (opts.mode() === 'create' || !backend) return
    testResult.value = null
    testing.value = true
    try {
      testResult.value = await testStorageBackend(backend.name)
      await opts.reload()
    } catch (err) {
      testResult.value = { ok: false, message: err instanceof Error ? err.message : t('adm.storage.testFailed') }
    } finally {
      testing.value = false
    }
  }

  async function save(): Promise<void> {
    if (!form.name || !form.kind || saving.value) return
    saving.value = true
    try {
      const backend = opts.backend()
      if (opts.mode() === 'create') {
        await createStorageBackend({
          name: form.name,
          kind: form.kind,
          is_default: form.is_default,
          config: submittableConfig(form.config),
        })
        toasts.success(tp('adm.storage.pair', { label: t('adm.storage.created'), value: form.name }))
      } else if (backend) {
        await updateStorageBackend(backend.name, {
          is_default: form.is_default,
          config: submittableConfig(form.config),
        })
        toasts.success(tp('adm.storage.pair', { label: t('adm.storage.updated'), value: form.name }))
      }
      opts.close()
      await opts.reload()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.storage.saveFailed'))
    } finally {
      saving.value = false
    }
  }

  return { form, fields, revealedSecrets, testResult, testing, saving, init, selectType, test, save }
}
