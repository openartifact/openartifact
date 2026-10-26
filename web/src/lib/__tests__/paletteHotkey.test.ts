// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { isApplePlatform, isPaletteHotkey, isPaletteSlashHotkey, isTypingTarget, paletteHotkeyLabel, paletteHotkeyShortcut } from '../paletteHotkey'

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

describe('isPaletteSlashHotkey', () => {
  const slash = (target: EventTarget | null, extra: Record<string, unknown> = {}) =>
    isPaletteSlashHotkey({ key: '/', ctrlKey: false, metaKey: false, altKey: false, target, ...extra })

  it('opens from the page body and from buttons', () => {
    expect(slash(document.body)).toBe(true)
    expect(slash(document.createElement('button'))).toBe(true)
    const checkbox = document.createElement('input')
    checkbox.type = 'checkbox'
    expect(slash(checkbox)).toBe(true)
  })

  it('stays a character while typing in a field', () => {
    expect(slash(document.createElement('input'))).toBe(false)
    const search = document.createElement('input')
    search.type = 'search'
    expect(slash(search)).toBe(false)
    expect(slash(document.createElement('textarea'))).toBe(false)
    expect(slash(document.createElement('select'))).toBe(false)
    const editable = document.createElement('div')
    Object.defineProperty(editable, 'isContentEditable', { value: true })
    expect(slash(editable)).toBe(false)
    expect(isTypingTarget(null)).toBe(false)
  })

  it('ignores other keys, modified slashes and IME composition', () => {
    expect(slash(document.body, { key: 'a' })).toBe(false)
    expect(slash(document.body, { ctrlKey: true })).toBe(false)
    expect(slash(document.body, { metaKey: true })).toBe(false)
    expect(slash(document.body, { altKey: true })).toBe(false)
    expect(slash(document.body, { isComposing: true })).toBe(false)
  })
})
