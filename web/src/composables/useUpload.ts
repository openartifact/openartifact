// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, computed } from 'vue'
import { ApiError, csrfHeader, expireSession } from '../api/client'

// Mirrors request()'s error handling for XHR uploads: JSON `{error, code}`
// bodies become the message instead of the raw response text.
function parseXhrError(xhr: XMLHttpRequest): ApiError {
  let message = `HTTP ${xhr.status}`
  let code: string | undefined
  try {
    const body = JSON.parse(xhr.responseText) as { error?: string; code?: string }
    if (body.error) message = body.error
    if (body.code) code = body.code
  } catch {
    // non-JSON body: keep the HTTP status message
  }
  return new ApiError(message, xhr.status, code)
}

export type UploadStatus = 'queued' | 'uploading' | 'complete' | 'failed' | 'cancelled'

export interface UploadEntry {
  id: number
  file: File
  status: UploadStatus
  loaded: number
  total: number
  error?: string
  xhr?: XMLHttpRequest
}

interface UploadStartOptions {
  url: string
  field?: string
  headers?: Record<string, string>
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
    return entry
  }

  function start(entry: UploadEntry, opts: UploadStartOptions): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      entry.xhr = xhr
      entry.status = 'uploading'
      xhr.open('POST', opts.url)
      xhr.withCredentials = true
      xhr.setRequestHeader('X-Requested-With', 'XMLHttpRequest')
      for (const [k, v] of Object.entries(csrfHeader())) xhr.setRequestHeader(k, v)
      for (const [k, v] of Object.entries(opts.headers ?? {})) xhr.setRequestHeader(k, v)
      xhr.upload.addEventListener('progress', (ev) => {
        if (ev.lengthComputable) {
          entry.loaded = ev.loaded
          entry.total = ev.total
        }
      })
      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          entry.status = 'complete'
          entry.loaded = entry.total
          resolve()
          return
        }
        const err = parseXhrError(xhr)
        entry.status = 'failed'
        entry.error = err.message
        if (xhr.status === 401) void expireSession()
        reject(err)
      })
      xhr.addEventListener('error', () => {
        entry.status = 'failed'
        entry.error = 'network error'
        reject(new ApiError('network error', 0))
      })
      xhr.addEventListener('abort', () => {
        if (entry.status !== 'complete' && entry.status !== 'failed') entry.status = 'cancelled'
        reject(new ApiError('aborted', 0))
      })
      const form = new FormData()
      form.append(opts.field ?? 'file', entry.file, entry.file.name)
      xhr.send(form)
    })
  }

  function cancel(entry: UploadEntry): void {
    if (entry.xhr && (entry.status === 'uploading' || entry.status === 'queued')) {
      try {
        entry.xhr.abort()
      } catch {
        // ignore
      }
    }
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
