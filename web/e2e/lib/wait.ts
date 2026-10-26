// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { setTimeout as sleep } from 'node:timers/promises'

export const DEFAULT_TIMEOUT_MS = 10_000

/**
 * Retries `check` until it returns without throwing, or the deadline passes
 * (then the last error is rethrown, with the elapsed time). The pause between
 * attempts only paces the polling; the wait itself ends on the condition.
 */
export async function eventually<T>(check: () => T | Promise<T>, timeoutMs: number = DEFAULT_TIMEOUT_MS, intervalMs = 50): Promise<T> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    try {
      return await check()
    } catch (err) {
      if (Date.now() >= deadline) {
        const last = err instanceof Error ? err : new Error(String(err))
        last.message = `${last.message}\n(still failing after ${timeoutMs} ms)`
        throw last
      }
    }
    await sleep(intervalMs)
  }
}
