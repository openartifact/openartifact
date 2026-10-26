// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ApiError } from '../api/client'

/**
 * The state of one independent dashboard source. A block that the caller may not
 * read (`forbidden`) is left out of the page; one that failed (`error`) says so.
 */
export type Block<T> =
  | { state: 'loading' }
  | { state: 'ready'; data: T }
  | { state: 'forbidden' }
  | { state: 'error'; message: string }

export const loadingBlock = <T>(): Block<T> => ({ state: 'loading' })

/** Runs a loader and folds its outcome into a block: 401/403/404/501 are "not for this caller". */
export async function toBlock<T>(load: () => Promise<T>): Promise<Block<T>> {
  try {
    return { state: 'ready', data: await load() }
  } catch (err) {
    if (err instanceof ApiError && [401, 403, 404, 501].includes(err.status)) return { state: 'forbidden' }
    return { state: 'error', message: err instanceof Error ? err.message : String(err) }
  }
}

/** The data of a ready block, otherwise undefined. */
export function dataOf<T>(block: Block<T>): T | undefined {
  return block.state === 'ready' ? block.data : undefined
}
