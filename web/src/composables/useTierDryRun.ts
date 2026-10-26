// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref } from 'vue'
import { dryRun, type StoragePolicyInput, type TierDryRun } from '../api/tiering'
import { t } from '../i18n'

const SAMPLE_LIMIT = 25

/**
 * One simulation slot: `run()` simulates a draft policy, or every enabled
 * saved policy when called without one. A response that arrives after a newer
 * `run()`/`reset()` is dropped so a slow answer never overwrites a fresher one.
 */
export function useTierDryRun() {
  const result = ref<TierDryRun | null>(null)
  const loading = ref(false)
  const error = ref('')
  let latest = 0

  async function run(policy?: StoragePolicyInput): Promise<void> {
    const id = ++latest
    loading.value = true
    error.value = ''
    try {
      const res = await dryRun({ ...(policy ? { policy } : {}), sample_limit: SAMPLE_LIMIT })
      if (id === latest) result.value = res
    } catch (err) {
      if (id !== latest) return
      result.value = null
      error.value = err instanceof Error ? err.message : t('tiering.dryRun.failed')
    } finally {
      if (id === latest) loading.value = false
    }
  }

  function reset(): void {
    latest++
    result.value = null
    error.value = ''
    loading.value = false
  }

  return { result, loading, error, run, reset }
}
