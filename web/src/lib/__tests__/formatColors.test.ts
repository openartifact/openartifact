// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { ALL_FORMATS, BADGE_SURFACE, FORMAT_META, formatColor, formatIcon, formatThemeColors, hexContrast, ICON_MIN_CONTRAST } from '../formats'

describe('format icon colors', () => {
  it('every format has an icon, a label and a #rrggbb brand color', () => {
    for (const id of ALL_FORMATS) {
      expect(formatIcon(id), id).toBeTruthy()
      expect(FORMAT_META[id].label, id).toBeTruthy()
      expect(formatColor(id), id).toMatch(/^#[0-9A-Fa-f]{6}$/)
    }
  })

  it('hold 3:1 on the badge surface in both themes (WCAG 1.4.11)', () => {
    for (const id of ALL_FORMATS) {
      const c = formatThemeColors(id)
      expect(hexContrast(c.light, BADGE_SURFACE.light), `${id} light ${c.light}`).toBeGreaterThanOrEqual(ICON_MIN_CONTRAST)
      expect(hexContrast(c.dark, BADGE_SURFACE.dark), `${id} dark ${c.dark}`).toBeGreaterThanOrEqual(ICON_MIN_CONTRAST)
    }
  })

  it('keep a brand color that already reads, and keep the hue of one that is adjusted', () => {
    // Ansible red passes on both surfaces untouched.
    expect(formatThemeColors('ansible').light).toBe('#ee0000')
    // Helm navy is invisible on dark: it turns into a lighter blue, not white.
    const helm = formatThemeColors('helm').dark
    expect(helm).not.toBe('#0f1689')
    const [r, , b] = [1, 3, 5].map((i) => Number.parseInt(helm.slice(i, i + 2), 16))
    expect(b!).toBeGreaterThan(r!)
  })

  it('measures contrast like WCAG', () => {
    expect(hexContrast('#000000', '#ffffff')).toBeCloseTo(21, 5)
    expect(hexContrast('#777777', '#777777')).toBe(1)
  })
})
