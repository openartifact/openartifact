// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n'
import type { Transport } from '../api/client'
import { seedState } from './state'
import { ROUTES } from './routes'
import { jsonResponse, type DemoHandler, type DemoRoute } from './http'

/** Error code of every call the demo does not answer. */
export const DEMO_UNAVAILABLE = 'demo_unavailable'

/** The answer of a call the demo has no data for: the error every page renders the same way. */
function unavailable(): Response {
  return jsonResponse(501, { error: t('shell.demo.unavailable'), code: DEMO_UNAVAILABLE })
}

interface CompiledRoute {
  method: string
  pattern: RegExp
  keys: string[]
  handler: DemoHandler
}

function compile([method, template, handler]: DemoRoute): CompiledRoute {
  const keys: string[] = []
  const source = template
    .split('/')
    .map((seg) => {
      if (seg.startsWith(':')) {
        keys.push(seg.slice(1))
        return '([^/]+)'
      }
      return seg.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    })
    .join('/')
  return { method, pattern: new RegExp(`^${source}$`), keys, handler }
}

function parseBody(body: BodyInit | null | undefined): unknown {
  if (typeof body !== 'string') return undefined
  try {
    return JSON.parse(body)
  } catch {
    return undefined
  }
}

export interface DemoBackend {
  transport: Transport
  /** `METHOD path` of every call no route answered, in order. */
  readonly unhandled: string[]
}

/** An in-memory server over freshly seeded state: writes change it until the page reloads. */
export function createDemoBackend(): DemoBackend {
  const compiled = ROUTES.map(compile)
  const state = seedState()
  const unhandled: string[] = []

  const transport: Transport = async (path, init) => {
    const url = new URL(path, 'http://demo.invalid')
    const method = (init.method ?? 'GET').toUpperCase()
    for (const route of compiled) {
      if (route.method !== method) continue
      const m = route.pattern.exec(url.pathname)
      if (!m) continue
      const params = Object.fromEntries(route.keys.map((k, i) => [k, decodeURIComponent(m[i + 1] ?? '')]))
      const out = route.handler({ params, query: url.searchParams, body: parseBody(init.body), state })
      if (out instanceof Response) return out
      return out === undefined ? new Response(null, { status: 204 }) : jsonResponse(200, out)
    }
    unhandled.push(`${method} ${url.pathname}`)
    return unavailable()
  }

  return { transport, unhandled }
}
