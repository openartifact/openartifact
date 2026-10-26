// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { onMounted, ref } from 'vue'
import { getSecurityHealth, type SecurityHealth } from '../api/security'

/** Loads the scanning pipeline health once on mount; `load()` refreshes it. */
export function useSecurityHealth(fallbackError: string) {
  const health = ref<SecurityHealth | null>(null)
  const loading = ref(true)
  const error = ref('')

  async function load(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      health.value = await getSecurityHealth()
    } catch (err) {
      error.value = err instanceof Error ? err.message : fallbackError
    } finally {
      loading.value = false
    }
  }

  onMounted(load)
  return { health, loading, error, load }
}
