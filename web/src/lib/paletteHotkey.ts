// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n/en'

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
