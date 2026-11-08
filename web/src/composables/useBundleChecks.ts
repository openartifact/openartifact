// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, shallowRef, watch, type Ref } from 'vue'
import { listBundleChecks, type BundleCheck } from '../api/bundles'
import { t } from '../i18n'

export interface BundleChecks {
  checks: Ref<BundleCheck[]>
  loading: Ref<boolean>
  /** Why the checks could not be loaded; empty otherwise. */
  error: Ref<string>
  reload(): Promise<void>
}

/** The checks reported against a bundle. They only decorate the page, so a failure is shown in the panel, not as a page error. */
export function useBundleChecks(name: () => string, version: () => string): BundleChecks {
  const checks = shallowRef<BundleCheck[]>([])
  const loading = ref(false)
  const error = ref('')
  let generation = 0

  async function reload(): Promise<void> {
    const mine = ++generation
    loading.value = true
    try {
      const rows = await listBundleChecks(name(), version())
      if (mine !== generation) return
      checks.value = rows
      error.value = ''
    } catch (err) {
      if (mine !== generation) return
      checks.value = []
      error.value = err instanceof Error ? err.message : t('bundles.checks.loadFailed')
    } finally {
      if (mine === generation) loading.value = false
    }
  }

  watch([name, version], () => void reload(), { immediate: true })

  return { checks, loading, error, reload }
}
