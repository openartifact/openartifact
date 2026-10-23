// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { captureInstallPrompt, useInstallPrompt } from '../useInstallPrompt'

function promptEvent() {
  const e = new Event('beforeinstallprompt', { cancelable: true }) as Event & {
    prompt: () => Promise<void>
    userChoice: Promise<{ outcome: string }>
    prompted: number
  }
  e.prompted = 0
  e.prompt = () => {
    e.prompted++
    return Promise.resolve()
  }
  e.userChoice = Promise.resolve({ outcome: 'accepted' })
  return e
}

describe('useInstallPrompt', () => {
  it('is hidden until the browser offers an install, then single-use', async () => {
    const stop = captureInstallPrompt(window)
    try {
      const { canInstall, install } = useInstallPrompt()
      expect(canInstall.value).toBe(false)

      const e = promptEvent()
      window.dispatchEvent(e)
      expect(e.defaultPrevented).toBe(true)
      expect(canInstall.value).toBe(true)

      await install()
      expect(e.prompted).toBe(1)
      expect(canInstall.value).toBe(false)

      await install()
      expect(e.prompted).toBe(1)
    } finally {
      stop()
    }
  })

  it('hides the offer once the app is installed', () => {
    const stop = captureInstallPrompt(window)
    try {
      const { canInstall } = useInstallPrompt()
      window.dispatchEvent(promptEvent())
      expect(canInstall.value).toBe(true)
      window.dispatchEvent(new Event('appinstalled'))
      expect(canInstall.value).toBe(false)
    } finally {
      stop()
    }
  })
})
