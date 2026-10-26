// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n'

interface NavigatorWithUAData extends Navigator {
  userAgentData?: { platform?: string }
}

/** True on macOS and iOS, where the palette hotkey is Cmd+K rather than Ctrl+K. */
export function isApplePlatform(nav: NavigatorWithUAData = navigator): boolean {
  const platform = nav.userAgentData?.platform || nav.platform || ''
  return /mac|iphone|ipad|ipod/i.test(platform)
}

/** Visible hint for the palette hotkey, e.g. `⌘K` or `Ctrl K`. */
export function paletteHotkeyLabel(apple: boolean = isApplePlatform()): string {
  return apple ? t('shell.paletteHotkey.apple') : t('shell.paletteHotkey.other')
}

/** Value of `aria-keyshortcuts` for the palette trigger. */
export function paletteHotkeyShortcut(apple: boolean = isApplePlatform()): string {
  return apple ? 'Meta+K' : 'Control+K'
}

/**
 * Whether a keydown is the palette hotkey: K with Ctrl or Cmd and no other
 * modifier, so Ctrl+Shift+K and Alt combinations stay available to the browser
 * and to AltGr layouts.
 */
export function isPaletteHotkey(e: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'altKey'>): boolean {
  return (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'k'
}

interface HotkeyEvent extends Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey'> {
  target: EventTarget | null
  isComposing?: boolean
}

/** Whether keystrokes at `target` are text being typed (so a bare "/" must stay a character). */
export function isTypingTarget(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  if (!el || typeof el.tagName !== 'string') return false
  if (el.isContentEditable) return true
  const tag = el.tagName
  if (tag === 'TEXTAREA' || tag === 'SELECT') return true
  if (tag !== 'INPUT') return false
  // Buttons, checkboxes and the like take no text: "/" may open the palette from them.
  return !['button', 'checkbox', 'radio', 'submit', 'reset', 'range', 'color', 'file', 'image'].includes(
    (el as HTMLInputElement).type,
  )
}

/**
 * Whether a keydown is the bare "/" shortcut: no Ctrl/Cmd/Alt, not while the
 * user is typing in a field and not while an IME composition is running.
 */
export function isPaletteSlashHotkey(e: HotkeyEvent): boolean {
  return e.key === '/' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.isComposing && !isTypingTarget(e.target)
}
