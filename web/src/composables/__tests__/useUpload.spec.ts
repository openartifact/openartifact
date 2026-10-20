// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, mock } from 'bun:test'
import { useUpload } from '../useUpload'

interface MockXHRListeners {
  load?: () => void
  error?: () => void
  abort?: () => void
  progress?: (ev: { lengthComputable: boolean; loaded: number; total: number }) => void
}

class MockXHR {
  status = 200
  responseText = ''
  withCredentials = false
  upload = {
    listeners: {} as MockXHRListeners,
    addEventListener(name: keyof MockXHRListeners, fn: MockXHRListeners[keyof MockXHRListeners]) {
      ;(this.listeners as Record<string, unknown>)[name] = fn
    },
  }
  listeners: MockXHRListeners = {}
  open = mock()
  setRequestHeader = mock()
  send = mock()
  abort() {
    this.listeners.abort?.()
  }
  addEventListener<K extends keyof MockXHRListeners>(name: K, fn: MockXHRListeners[K]) {
    this.listeners[name] = fn as never
  }
  // Helpers used by tests:
  fireProgress(loaded: number, total: number) {
    this.upload.listeners.progress?.({ lengthComputable: true, loaded, total })
  }
  fireLoad(status = 200, body = '') {
    this.status = status
    this.responseText = body
    this.listeners.load?.()
  }
  fireError() {
    this.listeners.error?.()
  }
}

let lastXhr: MockXHR | null = null
const originalXMLHttpRequest = globalThis.XMLHttpRequest

beforeEach(() => {
  lastXhr = null
  globalThis.XMLHttpRequest = mock(function () {
    lastXhr = new MockXHR()
    return lastXhr
  }) as any
  // FormData is provided by happy-dom.
})

afterEach(() => {
  globalThis.XMLHttpRequest = originalXMLHttpRequest
})

describe('useUpload', () => {
  it('reports progress through the entry', async () => {
    const { add, start } = useUpload()
    const file = new File(['hello'], 'a.txt', { type: 'text/plain' })
    const entry = add(file)
    const promise = start(entry, { url: '/upload' })
    expect(lastXhr).not.toBeNull()
    lastXhr!.fireProgress(2, 5)
    expect(entry.loaded).toBe(2)
    expect(entry.total).toBe(5)
    lastXhr!.fireLoad(200)
    await promise
    expect(entry.status).toBe('complete')
  })

  it('marks entry failed and rejects on non-2xx', async () => {
    const { add, start } = useUpload()
    const file = new File(['x'], 'b.bin')
    const entry = add(file)
    const promise = start(entry, { url: '/upload' })
    lastXhr!.fireLoad(500, JSON.stringify({ error: 'oops', code: 'internal' }))
    await expect(promise).rejects.toBeDefined()
    expect(entry.status).toBe('failed')
    expect(entry.error).toBe('oops')
  })

  it('cancel triggers abort and marks entry cancelled', async () => {
    const { add, start, cancel } = useUpload()
    const file = new File(['x'], 'c.bin')
    const entry = add(file)
    const promise = start(entry, { url: '/upload' })
    cancel(entry)
    await expect(promise).rejects.toBeDefined()
    expect(entry.status).toBe('cancelled')
  })

  it('does not overwrite completed or failed entries on cancel', async () => {
    const { add, start, cancel } = useUpload()
    const done = add(new File(['x'], 'd.bin'))
    const p1 = start(done, { url: '/upload' })
    lastXhr!.fireLoad(200)
    await p1
    cancel(done)
    expect(done.status).toBe('complete')

    const bad = add(new File(['x'], 'e.bin'))
    const p2 = start(bad, { url: '/upload' })
    lastXhr!.fireLoad(500, JSON.stringify({ error: 'nope' }))
    await expect(p2).rejects.toBeDefined()
    cancel(bad)
    expect(bad.status).toBe('failed')
  })

  it('falls back to the HTTP status for non-JSON error bodies', async () => {
    const { add, start } = useUpload()
    const entry = add(new File(['x'], 'f.bin'))
    const promise = start(entry, { url: '/upload' })
    lastXhr!.fireLoad(502, '<html>bad gateway</html>')
    await expect(promise).rejects.toBeDefined()
    expect(entry.error).toBe('HTTP 502')
  })
})
