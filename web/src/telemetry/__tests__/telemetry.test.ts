// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, afterAll, spyOn } from 'bun:test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { ApiError, request } from '../../api/client'
import { startTelemetry, stopTelemetry } from '..'
import { exceptionAttributes, initTelemetry, traceparent } from '../otel'

const WEB = join(import.meta.dir, '..', '..', '..')
const TRACEPARENT = /^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$/

/** The headers of the n-th fetch call of spy. */
function headersOf(spy: { mock: { calls: unknown[][] } }, n: number): Headers {
  return (spy.mock.calls[n]?.[1] as RequestInit & { headers: Headers }).headers
}

describe('startTelemetry', () => {
  // Some tests install the wrapper for real; leave no span factory behind
  // for the test files that run afterwards.
  afterAll(stopTelemetry)

  it('loads nothing while the server has frontend telemetry off', async () => {
    const fetch = spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response('{"error":"not found"}', { status: 404 }))
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))

    await startTelemetry()
    await request('/api/v1/test')

    expect(fetch.mock.calls[0]?.[0]).toBe('/api/v1/telemetry/frontend')
    expect(headersOf(fetch, 1).has('traceparent')).toBe(false)
  })

  it('starts the wrapper when the server enables it, and then traces API calls', async () => {
    const fetch = spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response(JSON.stringify({ enabled: true, sample_ratio: 1 }), { status: 200 }))
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))

    await startTelemetry()
    await request('/api/v1/test')

    expect(headersOf(fetch, 1).get('traceparent')).toMatch(/-01$/)
  })

  it('stops on sign-out and starts again on the next sign-in', async () => {
    const enabled = () => new Response(JSON.stringify({ enabled: true, sample_ratio: 1 }), { status: 200 })
    const fetch = spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(enabled())
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))
      .mockResolvedValueOnce(enabled())
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))

    stopTelemetry()
    await startTelemetry()
    await request('/api/v1/test')
    expect(headersOf(fetch, 1).has('traceparent')).toBe(true)

    stopTelemetry()
    await request('/api/v1/test')
    expect(headersOf(fetch, 2).has('traceparent')).toBe(false)

    await startTelemetry()
    await request('/api/v1/test')
    expect(headersOf(fetch, 4).has('traceparent')).toBe(true)
  })

  it('does not start when the user signs out while the settings are loading', async () => {
    stopTelemetry()
    const fetch = spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response(JSON.stringify({ enabled: true, sample_ratio: 1 }), { status: 200 }))
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))

    const starting = startTelemetry()
    stopTelemetry()
    await starting
    await request('/api/v1/test')

    expect(headersOf(fetch, 1).has('traceparent')).toBe(false)
  })
})

describe('telemetry wrapper', () => {
  it('samples client spans with the server ratio and propagates the decision', () => {
    for (const [ratio, flags] of [
      [1, '01'],
      [0, '00'],
    ] as const) {
      const span = initTelemetry({ enabled: true, sample_ratio: ratio }).startClientSpan('get')
      expect(span.traceparent).toMatch(TRACEPARENT)
      expect(span.traceparent.endsWith(`-${flags}`)).toBe(true)
      span.end(200)
    }
  })

  it('formats a W3C traceparent', () => {
    expect(traceparent({ traceId: '0af7651916cd43dd8448eb211c80319c', spanId: 'b7ad6b7169203331', traceFlags: 1 })).toBe(
      '00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01',
    )
  })

  it('records caught errors with the exception attributes of the errors instrumentation', () => {
    const err = new TypeError('boom')
    expect(exceptionAttributes(err)).toEqual({ 'exception.type': 'TypeError', 'exception.message': 'boom', 'exception.stacktrace': err.stack })
    expect(exceptionAttributes('plain')).toEqual({ 'exception.message': 'plain' })
  })

  it('exports only the type of an ApiError, never its server text or stack', () => {
    const err = new ApiError('repository "acme-secret" not found', 404, 'not_found')
    expect(err.stack).toContain('acme-secret')
    expect(exceptionAttributes(err)).toEqual({ 'exception.type': 'ApiError' })
  })

  it('uses errors and web vitals only', () => {
    const source = readFileSync(join(WEB, 'src', 'telemetry', 'otel.ts'), 'utf8')
    const imported = [...source.matchAll(/from '(@opentelemetry\/[^']+)'/g)].map((m) => m[1]).sort()
    expect(imported).toEqual([
      '@opentelemetry/api',
      '@opentelemetry/browser-instrumentation/experimental/errors',
      '@opentelemetry/browser-instrumentation/experimental/web-vitals',
      '@opentelemetry/exporter-logs-otlp-proto',
      '@opentelemetry/exporter-trace-otlp-proto',
      '@opentelemetry/resources',
      '@opentelemetry/sdk-logs',
      '@opentelemetry/sdk-trace',
    ])
  })
})

describe('OpenTelemetry pins', () => {
  const pkg = JSON.parse(readFileSync(join(WEB, 'package.json'), 'utf8')) as { dependencies: Record<string, string> }
  const lock = readFileSync(join(WEB, 'bun.lock'), 'utf8')
  const pinned = Object.entries(pkg.dependencies).filter(([name]) => name.startsWith('@opentelemetry/'))

  it('pins every OpenTelemetry package to an exact version', () => {
    expect(pinned.map(([name]) => name).sort()).toEqual([
      '@opentelemetry/api',
      '@opentelemetry/browser-instrumentation',
      '@opentelemetry/exporter-logs-otlp-proto',
      '@opentelemetry/exporter-trace-otlp-proto',
      '@opentelemetry/resources',
      '@opentelemetry/sdk-logs',
      '@opentelemetry/sdk-trace',
    ])
    for (const [, version] of pinned) expect(version).toMatch(/^\d+\.\d+\.\d+$/)
  })

  it('resolves each pin to that version in bun.lock', () => {
    for (const [name, version] of pinned) {
      expect(lock).toContain(`"${name}": "${version}"`)
      expect(lock).toContain(`"${name}": ["${name}@${version}"`)
    }
  })
})
