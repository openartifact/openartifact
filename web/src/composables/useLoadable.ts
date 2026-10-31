// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, shallowRef, type Ref, type ShallowRef } from 'vue'

export interface Loadable<T> {
  data: ShallowRef<T>
  loading: Ref<boolean>
  error: Ref<string>
  /** Loads again; the latest call wins when several overlap. */
  reload(): Promise<void>
}

/** A value fetched on setup and on demand, with its loading and error state; `initial` stands until the first load ends. */
export function useLoadable<T>(load: () => Promise<T>, initial: T): Loadable<T> {
  const data = shallowRef<T>(initial)
  const loading = ref(true)
  const error = ref('')
  let latest = 0

  async function reload(): Promise<void> {
    const call = ++latest
    loading.value = true
    error.value = ''
    try {
      const value = await load()
      if (call === latest) data.value = value
    } catch (err) {
      if (call === latest) error.value = err instanceof Error ? err.message : String(err)
    } finally {
      if (call === latest) loading.value = false
    }
  }

  void reload()
  return { data, loading, error, reload }
}
