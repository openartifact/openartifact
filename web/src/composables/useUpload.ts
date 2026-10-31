// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, computed } from 'vue'
import { upload } from '../api/client'

export type UploadStatus = 'queued' | 'uploading' | 'complete' | 'failed' | 'cancelled'

export interface UploadEntry {
  id: number
  file: File
  status: UploadStatus
  loaded: number
  total: number
  error?: string
  abort?: AbortController
}

interface UploadStartOptions {
  url: string
  field?: string
  /** Extra multipart text fields sent next to the file. */
  fields?: Record<string, string>
  headers?: Record<string, string>
  /** `PUT` sends the file itself as the request body (native format routes) instead of a multipart form. */
  method?: 'POST' | 'PUT'
}

let nextId = 1

export function useUpload() {
  const entries = ref<UploadEntry[]>([])

  const inProgress = computed(() => entries.value.some((e) => e.status === 'uploading' || e.status === 'queued'))

  function add(file: File): UploadEntry {
    const entry: UploadEntry = {
      id: nextId++,
      file,
      status: 'queued',
      loaded: 0,
      total: file.size,
    }
    entries.value.push(entry)
    // Hand back the reactive proxy: later status/progress writes on the raw object would never reach the view.
    return entries.value[entries.value.length - 1]
  }

  function start(entry: UploadEntry, opts: UploadStartOptions): Promise<void> {
    const abort = new AbortController()
    entry.abort = abort
    entry.status = 'uploading'
    let body: XMLHttpRequestBodyInit
    if (opts.method === 'PUT') {
      body = entry.file
    } else {
      const form = new FormData()
      form.append(opts.field ?? 'file', entry.file, entry.file.name)
      for (const [k, v] of Object.entries(opts.fields ?? {})) form.append(k, v)
      body = form
    }
    return upload(opts.url, {
      method: opts.method,
      body,
      headers: opts.headers,
      signal: abort.signal,
      onProgress(loaded, total) {
        entry.loaded = loaded
        entry.total = total
      },
    }).then(
      () => {
        entry.status = 'complete'
        entry.loaded = entry.total
      },
      (err: unknown) => {
        if (abort.signal.aborted) {
          if (entry.status !== 'complete' && entry.status !== 'failed') entry.status = 'cancelled'
        } else {
          entry.status = 'failed'
          entry.error = err instanceof Error ? err.message : String(err)
        }
        throw err
      },
    )
  }

  function cancel(entry: UploadEntry): void {
    if (entry.abort && (entry.status === 'uploading' || entry.status === 'queued')) entry.abort.abort()
    // Only entries that are still pending can be cancelled; completed and
    // failed ones keep their final status.
    if (entry.status === 'uploading' || entry.status === 'queued') entry.status = 'cancelled'
  }

  function cancelAll(): void {
    for (const e of entries.value) cancel(e)
  }

  function clearCompleted(): void {
    entries.value = entries.value.filter((e) => e.status !== 'complete')
  }

  function reset(): void {
    cancelAll()
    entries.value = []
  }

  return { entries, inProgress, add, start, cancel, cancelAll, clearCompleted, reset }
}
