// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DemoState } from './state'

/** One API call as a handler sees it. */
export interface DemoRequest {
  /** `:name` segments of the route template, decoded. */
  params: Record<string, string>
  query: URLSearchParams
  /** The parsed JSON body; undefined for none or a non-JSON body. */
  body: unknown
  state: DemoState
}

/** The JSON answer (200), undefined for 204, or a ready Response (another status). */
export type DemoHandler = (req: DemoRequest) => unknown

/** `[method, template, handler]`; a template segment `:name` matches one path segment. */
export type DemoRoute = readonly [method: string, template: string, handler: DemoHandler]

/** The 200 body of a generated `<Operation>Responses` type: handlers return it, so an API change breaks the typecheck. */
export type Ok<T extends { 200: unknown }> = T[200]

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** A handler's refusal with the server's `{error, code}` shape. */
export function failure(status: number, error: string, code: string): Response {
  return jsonResponse(status, { error, code })
}
