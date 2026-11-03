// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it } from 'bun:test'
import { isTabHidden, skipWhileHidden } from '../visibility'

function setVisibility(state: 'visible' | 'hidden'): void {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
}

afterEach(() => setVisibility('visible'))

describe('skipWhileHidden', () => {
  it('runs the tick while the tab is shown and not while it is hidden', () => {
    let runs = 0
    const tick = skipWhileHidden(() => runs++)
    tick()
    setVisibility('hidden')
    expect(isTabHidden()).toBe(true)
    tick()
    tick()
    setVisibility('visible')
    tick()
    expect(runs).toBe(2)
  })
})
