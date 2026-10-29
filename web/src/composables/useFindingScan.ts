// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, watch, type Ref } from 'vue'
import type { SecurityFinding } from '../api/security'

/**
 * Loads the scan behind the selected finding whenever it changes. A reply for a
 * finding that is no longer selected is dropped. `onLoaded` runs with each
 * accepted reply; `failed` is the message for a failure with no text of its own.
 */
export function useFindingScan<T>(
  finding: () => SecurityFinding,
  fetchScan: (finding: SecurityFinding) => Promise<T>,
  failed: () => string,
  onLoaded?: (scan: T) => void,
): { scan: Ref<T | null>; loading: Ref<boolean>; error: Ref<string> } {
  const scan = ref<T | null>(null) as Ref<T | null>
  const loading = ref(false)
  const error = ref('')
  let seq = 0

  watch(
    () => finding().id,
    async () => {
      const mine = ++seq
      scan.value = null
      error.value = ''
      loading.value = true
      try {
        const result = await fetchScan(finding())
        if (mine !== seq) return
        scan.value = result
        onLoaded?.(result)
      } catch (err) {
        if (mine === seq) error.value = err instanceof Error ? err.message : failed()
      } finally {
        if (mine === seq) loading.value = false
      }
    },
    { immediate: true },
  )

  return { scan, loading, error }
}
