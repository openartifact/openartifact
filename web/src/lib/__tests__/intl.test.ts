// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it } from 'bun:test'
import { setLocale } from '../../i18n'
import { compareText, formatLocale, formatNumber } from '../intl'

afterEach(() => setLocale('en'))

describe('Intl helpers follow the UI language', () => {
  it('formats numbers with the language grouping', async () => {
    expect(formatNumber(1234567.5)).toBe('1,234,567.5')
    await setLocale('de')
    expect(formatLocale()).toBe('de')
    expect(formatNumber(1234567.5)).toBe('1.234.567,5')
    await setLocale('fr')
    expect(formatNumber(1234.5)).toContain('1')
    expect(formatNumber(1234.5)).not.toBe('1,234.5')
  })

  it('treats a missing number as zero', () => {
    expect(formatNumber(null)).toBe('0')
    expect(formatNumber(undefined)).toBe('0')
  })

  it('sorts by the alphabet of the language, ignoring accents and case', async () => {
    expect(['Zebra', 'éclair', 'Eagle', 'dog'].sort(compareText)).toEqual(['dog', 'Eagle', 'éclair', 'Zebra'])
    await setLocale('de')
    expect(['b', 'a', 'B'].sort(compareText)[0]).toBe('a')
    expect(compareText('item2', 'item10')).toBeLessThan(0)
    expect(compareText(null, 'a')).toBeLessThan(0)
  })
})
