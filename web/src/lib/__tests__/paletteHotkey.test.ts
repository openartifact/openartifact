// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { isApplePlatform, isPaletteHotkey, paletteHotkeyLabel, paletteHotkeyShortcut } from '../paletteHotkey'

const key = (init: Partial<KeyboardEvent> & { key: string }) => ({
  ctrlKey: false,
  metaKey: false,
  shiftKey: false,
  altKey: false,
  ...init,
})

describe('isPaletteHotkey', () => {
  it('accepts Ctrl+K and Cmd+K in either case', () => {
    expect(isPaletteHotkey(key({ key: 'k', ctrlKey: true }))).toBe(true)
    expect(isPaletteHotkey(key({ key: 'K', ctrlKey: true }))).toBe(true)
    expect(isPaletteHotkey(key({ key: 'k', metaKey: true }))).toBe(true)
  })

  it('ignores K without Ctrl or Cmd', () => {
    expect(isPaletteHotkey(key({ key: 'k' }))).toBe(false)
  })

  it('leaves Shift and Alt combinations alone', () => {
    expect(isPaletteHotkey(key({ key: 'K', ctrlKey: true, shiftKey: true }))).toBe(false)
    expect(isPaletteHotkey(key({ key: 'k', ctrlKey: true, altKey: true }))).toBe(false)
    expect(isPaletteHotkey(key({ key: 'k', metaKey: true, shiftKey: true }))).toBe(false)
  })

  it('ignores other keys', () => {
    expect(isPaletteHotkey(key({ key: 'j', ctrlKey: true }))).toBe(false)
  })
})

describe('platform hint', () => {
  const nav = (platform: string, uaPlatform?: string) =>
    ({ platform, userAgentData: uaPlatform === undefined ? undefined : { platform: uaPlatform } }) as Navigator

  it('detects Apple platforms from the UA data first, then navigator.platform', () => {
    expect(isApplePlatform(nav('MacIntel'))).toBe(true)
    expect(isApplePlatform(nav('iPhone'))).toBe(true)
    expect(isApplePlatform(nav('', 'macOS'))).toBe(true)
    expect(isApplePlatform(nav('Linux x86_64'))).toBe(false)
    expect(isApplePlatform(nav('Win32', 'Windows'))).toBe(false)
  })

  it('labels the hotkey for the platform', () => {
    expect(paletteHotkeyLabel(true)).toBe('⌘K')
    expect(paletteHotkeyLabel(false)).toBe('Ctrl K')
    expect(paletteHotkeyShortcut(true)).toBe('Meta+K')
    expect(paletteHotkeyShortcut(false)).toBe('Control+K')
  })
})
