// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Experimental frontend telemetry (docs/operations/monitoring.md). This file is
// part of the main bundle and stays small: it asks the server whether the
// feature is on and only then imports the OpenTelemetry wrapper (./otel), a
// chunk of its own. While the feature is off nothing else is loaded or sent.

import { getFrontendTelemetry } from '../api/telemetry'
import { setClientSpanFactory } from '../api/client'

let recorder: ((err: unknown) => void) | undefined
let shutdown: (() => Promise<void>) | undefined
let starting: Promise<void> | undefined

/**
 * Loads and starts the telemetry wrapper once per page, if the server has the
 * feature on. Call it when a user signs in: the settings endpoint needs a
 * session. Never rejects; a 404 (feature off) or a network error leaves
 * telemetry off until the next call.
 */
export function startTelemetry(): Promise<void> {
  if (starting) return starting
  const run: Promise<void> = getFrontendTelemetry()
    .then(async (settings) => {
      const { initTelemetry } = await import('./otel')
      // Signed out while the settings or the chunk were loading.
      if (starting !== run) return
      const telemetry = initTelemetry(settings)
      setClientSpanFactory(telemetry.startClientSpan)
      recorder = telemetry.recordError
      shutdown = telemetry.shutdown
    })
    .catch(() => {
      if (starting === run) starting = undefined
    })
  starting = run
  return run
}

/**
 * Stops telemetry when the user signs out or the session ends: no more client
 * spans or captured errors, and the providers are shut down so nothing keeps
 * posting to the relay without a session. The next sign-in starts it again.
 */
export function stopTelemetry(): void {
  const stop = shutdown
  starting = undefined
  shutdown = undefined
  recorder = undefined
  setClientSpanFactory(undefined)
  void stop?.()
}

/** Records an error the app handled itself (Vue's error handler); a no-op while telemetry is off. */
export function recordError(err: unknown): void {
  recorder?.(err)
}
