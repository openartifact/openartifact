// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { effectScope, nextTick, ref } from 'vue'
import { useRestoreFocus } from '../useRestoreFocus'

describe('useRestoreFocus', () => {
  it('refocuses the element that was focused when it opened', async () => {
    const opener = document.createElement('button')
    const other = document.createElement('button')
    document.body.append(opener, other)
    opener.focus()

    const open = ref(false)
    const scope = effectScope()
    const { restoreFocus } = scope.run(() => useRestoreFocus(open))!
    open.value = true
    await nextTick()
    other.focus()
    open.value = false
    restoreFocus()
    await new Promise((resolve) => setTimeout(resolve, 5))

    expect(document.activeElement).toBe(opener)
    scope.stop()
    opener.remove()
    other.remove()
  })

  it('does nothing when the opener is gone', async () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const open = ref(true)
    const scope = effectScope()
    const { restoreFocus } = scope.run(() => useRestoreFocus(open))!
    opener.remove()
    restoreFocus()
    await new Promise((resolve) => setTimeout(resolve, 5))
    expect(document.activeElement).toBe(document.body)
    scope.stop()
  })
})
