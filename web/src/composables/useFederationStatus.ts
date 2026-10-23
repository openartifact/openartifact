// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { getCurrentScope, onScopeDispose, ref, watch, type Ref } from 'vue'
import { getFederationStatus, type FederationStatus } from '../api/federation'
import { FEDERATION_POLL_MS } from '../lib/federation'
import { t } from '../i18n/en'

/**
 * Loads the federation status of `repo` and re-reads it every `intervalMs` while the page is
 * visible. A new request is only scheduled after the previous one settled, a response that
 * arrives after the repository changed or a newer read is dropped, and the timer and the
 * visibility listener go away with the owning scope. A failed poll keeps the last good status.
 */
export function useFederationStatus(repo: () => string, intervalMs: number = FEDERATION_POLL_MS) {
  const status: Ref<FederationStatus | null> = ref(null)
  const loading = ref(true)
  const error = ref('')
  let timer: ReturnType<typeof setTimeout> | undefined
  let latest = 0
  let stopped = false

  function clearTimer(): void {
    clearTimeout(timer)
    timer = undefined
  }

  function schedule(): void {
    clearTimer()
    if (!stopped) timer = setTimeout(() => void tick(), intervalMs)
  }

  async function load(): Promise<void> {
    const id = ++latest
    clearTimer()
    try {
      const res = await getFederationStatus(repo())
      if (id !== latest) return
      status.value = res
      error.value = ''
    } catch (err) {
      if (id !== latest) return
      error.value = err instanceof Error ? err.message : t('federation.loadFailed')
    } finally {
      if (id === latest) {
        loading.value = false
        schedule()
      }
    }
  }

  async function tick(): Promise<void> {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
      schedule()
      return
    }
    await load()
  }

  /** Adopts a status returned by a mutation and restarts the poll period. */
  function apply(next: FederationStatus): void {
    latest++
    status.value = next
    error.value = ''
    loading.value = false
    schedule()
  }

  function onVisibility(): void {
    if (document.visibilityState === 'visible') void load()
  }

  function stop(): void {
    stopped = true
    latest++
    clearTimer()
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibility)
  }

  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibility)
  watch(
    repo,
    () => {
      status.value = null
      loading.value = true
      void load()
    },
    { immediate: true },
  )
  if (getCurrentScope()) onScopeDispose(stop)

  return { status, loading, error, reload: load, apply, stop }
}
