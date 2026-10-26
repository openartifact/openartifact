// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

export type Settled<T> = { ok: true; value: T } | { ok: false; error: unknown }

/** Runs `fn` over `items` with at most `concurrency` calls in flight; results keep the order of `items`, failures do not stop the others. */
export async function mapPool<I, O>(items: readonly I[], concurrency: number, fn: (item: I) => Promise<O>): Promise<Settled<O>[]> {
  const out: Settled<O>[] = new Array(items.length)
  let next = 0
  async function worker(): Promise<void> {
    while (next < items.length) {
      const i = next++
      try {
        out[i] = { ok: true, value: await fn(items[i]) }
      } catch (error) {
        out[i] = { ok: false, error }
      }
    }
  }
  await Promise.all(Array.from({ length: Math.max(1, Math.min(concurrency, items.length)) }, worker))
  return out
}
