// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { upload, ApiError, isSessionExpired } from '../client'
import router from '../../router'
import { spyOn } from 'bun:test'

// XMLHttpRequest is stubbed at the boundary; everything else is the real client.
class StubXHR {
  static last: StubXHR
  method = ''
  url = ''
  withCredentials = false
  status = 0
  responseText = ''
  headers: Record<string, string> = {}
  sent: unknown
  aborted = false
  private listeners: Record<string, () => void> = {}
  private progressListener?: (ev: { lengthComputable: boolean; loaded: number; total: number }) => void
  upload = {
    addEventListener: (_: string, fn: StubXHR['progressListener']) => {
      this.progressListener = fn
    },
  }
  constructor() {
    StubXHR.last = this
  }
  open(method: string, url: string) {
    this.method = method
    this.url = url
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k.toLowerCase()] = v
  }
  addEventListener(name: string, fn: () => void) {
    this.listeners[name] = fn
  }
  send(body: unknown) {
    this.sent = body
  }
  abort() {
    this.aborted = true
    this.listeners.abort?.()
  }
  progress(loaded: number, total: number, lengthComputable = true) {
    this.progressListener?.({ lengthComputable, loaded, total })
  }
  respond(status: number, body = '') {
    this.status = status
    this.responseText = body
    this.listeners.load?.()
  }
  fail() {
    this.listeners.error?.()
  }
}

const original = globalThis.XMLHttpRequest

describe('upload', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    globalThis.XMLHttpRequest = StubXHR as unknown as typeof XMLHttpRequest
    document.cookie = 'oa_csrf=tok123'
  })
  afterEach(() => {
    globalThis.XMLHttpRequest = original
    document.cookie = 'oa_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT'
  })

  it('sends the standard headers, credentials, method and body', async () => {
    const body = new Blob(['x'])
    const p = upload('/repo/a', { method: 'PUT', body, headers: { 'Content-Type': 'application/zip' } })
    const x = StubXHR.last
    expect(x.method).toBe('PUT')
    expect(x.url).toBe('/repo/a')
    expect(x.withCredentials).toBe(true)
    expect(x.headers['x-requested-with']).toBe('XMLHttpRequest')
    expect(x.headers['x-csrf-token']).toBe('tok123')
    expect(x.headers['content-type']).toBe('application/zip')
    expect(x.sent).toBe(body)
    x.respond(201)
    await p
  })

  it('defaults to POST', async () => {
    const p = upload('/u', { body: new FormData() })
    expect(StubXHR.last.method).toBe('POST')
    StubXHR.last.respond(200)
    await p
  })

  it('reports progress only when the total is computable', async () => {
    const seen: [number, number][] = []
    const p = upload('/u', { body: new FormData(), onProgress: (l, t) => seen.push([l, t]) })
    StubXHR.last.progress(2, 5)
    StubXHR.last.progress(3, 0, false)
    StubXHR.last.respond(200)
    await p
    expect(seen).toEqual([[2, 5]])
  })

  it('maps a JSON error body to an ApiError with its code', async () => {
    const p = upload('/u', { body: new FormData() })
    StubXHR.last.respond(409, JSON.stringify({ error: 'exists', code: 'conflict' }))
    const err = (await p.catch((e) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect([err.message, err.status, err.code]).toEqual(['exists', 409, 'conflict'])
  })

  it('falls back to the HTTP status for a non-JSON error body', async () => {
    const p = upload('/u', { body: new FormData() })
    StubXHR.last.respond(502, '<html>')
    expect(((await p.catch((e) => e)) as ApiError).message).toBe('HTTP 502')
  })

  it('maps a network failure to status 0', async () => {
    const p = upload('/u', { body: new FormData() })
    StubXHR.last.fail()
    const err = (await p.catch((e) => e)) as ApiError
    expect([err.message, err.status]).toEqual(['network error', 0])
  })

  it('aborts the request when the signal fires and rejects as aborted', async () => {
    const ctl = new AbortController()
    const p = upload('/u', { body: new FormData(), signal: ctl.signal })
    ctl.abort()
    const err = (await p.catch((e) => e)) as ApiError
    expect(StubXHR.last.aborted).toBe(true)
    expect([err.message, err.status]).toEqual(['aborted', 0])
  })

  it('does not send when the signal is already aborted', async () => {
    const ctl = new AbortController()
    ctl.abort()
    const p = upload('/u', { body: new FormData(), signal: ctl.signal })
    expect(StubXHR.last.sent).toBeUndefined()
    expect(((await p.catch((e) => e)) as ApiError).message).toBe('aborted')
  })

  it('treats a 401 as an expired session: redirects to login and throws', async () => {
    const push = spyOn(router, 'push').mockResolvedValue(undefined)
    const p = upload('/u', { body: new FormData() })
    StubXHR.last.respond(401, JSON.stringify({ error: 'nope' }))
    const err = await p.catch((e) => e)
    expect(isSessionExpired(err)).toBe(true)
    expect(push).toHaveBeenCalled()
  })
})
