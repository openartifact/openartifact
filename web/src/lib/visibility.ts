// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/** True while the tab is in the background (a poll then has nobody to show its result to). */
export function isTabHidden(): boolean {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden'
}

/** Wraps a poll tick so it does nothing while the tab is hidden; the next tick after it is shown runs normally. */
export function skipWhileHidden(tick: () => void): () => void {
  return () => {
    if (!isTabHidden()) tick()
  }
}
