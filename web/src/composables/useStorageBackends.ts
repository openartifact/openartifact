// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref } from 'vue'
import {
  deleteStorageBackend,
  listStorageBackends,
  testStorageBackend,
  testStoragePrimary,
  type StoragePrimary,
} from '../api/storage'
import { t, tp } from '../i18n'
import type { StorageBackend } from '../lib/storageBackends'
import { useToastStore } from '../stores/toast'

/** List state plus the test / delete actions of the storage backends page. */
export function useStorageBackends() {
  const toasts = useToastStore()
  const items = ref<StorageBackend[]>([])
  const primary = ref<StoragePrimary | null>(null)
  const loading = ref(true)
  const testingOne = ref<string | null>(null)
  const testingPrimary = ref(false)
  const testingAll = ref(false)

  const deleteTarget = ref<StorageBackend | null>(null)
  const deleteConfirmName = ref('')
  const deleting = ref(false)

  async function load(): Promise<void> {
    loading.value = true
    try {
      const res = await listStorageBackends()
      items.value = (res.items ?? []) as StorageBackend[]
      primary.value = res.primary ?? null
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.storage.loadFailed'))
    } finally {
      loading.value = false
    }
  }

  async function testOne(name: string): Promise<void> {
    testingOne.value = name
    try {
      const res = await testStorageBackend(name)
      if (res.ok) toasts.success(tp('adm.storage.pair', { label: name, value: t('adm.storage.status.healthy') }))
      else toasts.error(tp('adm.storage.pair', { label: name, value: res.message ?? t('adm.storage.testFailed') }))
      await load()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.storage.testFailed'))
    } finally {
      testingOne.value = null
    }
  }

  async function testPrimary(): Promise<void> {
    testingPrimary.value = true
    try {
      const res = await testStoragePrimary()
      const name = primary.value?.name ?? 'primary'
      if (res.ok) toasts.success(tp('adm.storage.pair', { label: name, value: t('adm.storage.status.healthy') }))
      else toasts.error(tp('adm.storage.pair', { label: name, value: res.message ?? t('adm.storage.testFailed') }))
      await load()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.storage.testFailed'))
    } finally {
      testingPrimary.value = false
    }
  }

  // The primary store is tested with the records. testStorageBackend resolves
  // with {ok:false} for failed checks, so failures are counted from the
  // results rather than from rejections.
  async function testAll(): Promise<void> {
    testingAll.value = true
    try {
      const checks = items.value.map((b) => testStorageBackend(b.name))
      if (primary.value) checks.push(testStoragePrimary())
      const results = await Promise.allSettled(checks)
      const failed = results.filter((r) => r.status === 'rejected' || !r.value.ok).length
      if (failed === 0) toasts.success(t('adm.storage.allHealthy'))
      else toasts.warning(`${failed} ${t('adm.storage.someFailed')}`)
      await load()
    } finally {
      testingAll.value = false
    }
  }

  function openDelete(backend: StorageBackend): void {
    deleteTarget.value = backend
    deleteConfirmName.value = ''
  }

  function cancelDelete(): void {
    deleteTarget.value = null
    deleteConfirmName.value = ''
  }

  async function confirmDelete(): Promise<void> {
    if (!deleteTarget.value || deleteConfirmName.value !== deleteTarget.value.name) return
    deleting.value = true
    try {
      await deleteStorageBackend(deleteTarget.value.name)
      toasts.success(tp('adm.storage.pair', { label: t('adm.storage.deleted'), value: deleteTarget.value.name }))
      deleteTarget.value = null
      deleteConfirmName.value = ''
      await load()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.storage.deleteFailed'))
    } finally {
      deleting.value = false
    }
  }

  return {
    items,
    primary,
    loading,
    testingOne,
    testingPrimary,
    testingAll,
    deleteTarget,
    deleteConfirmName,
    deleting,
    load,
    testOne,
    testPrimary,
    testAll,
    openDelete,
    cancelDelete,
    confirmDelete,
  }
}
