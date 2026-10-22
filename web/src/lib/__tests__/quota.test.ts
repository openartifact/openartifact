// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { formatBytes, percentOf, splitBytes, toBytes, usageBarClass, usageLevel } from '../quota'

describe('quota helpers', () => {
  it('formats byte sizes', () => {
    expect(formatBytes(12)).toBe('12 B')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(formatBytes(5 * 1024 ** 2)).toBe('5.0 MB')
    expect(formatBytes(3 * 1024 ** 3)).toBe('3.0 GB')
    expect(formatBytes(2 * 1024 ** 4)).toBe('2.0 TB')
  })

  it('splits a limit into the largest whole unit and back', () => {
    expect(splitBytes(0)).toEqual({ value: 0, unit: 'GB' })
    expect(splitBytes(512 * 1024 ** 2)).toEqual({ value: 512, unit: 'MB' })
    expect(splitBytes(1.5 * 1024 ** 3)).toEqual({ value: 1.5, unit: 'GB' })
    expect(splitBytes(2 * 1024 ** 4)).toEqual({ value: 2, unit: 'TB' })
    expect(toBytes(1.5, 'GB')).toBe(1.5 * 1024 ** 3)
    expect(toBytes(2, 'TB')).toBe(2 * 1024 ** 4)
  })

  it('treats an empty or non-positive size as unlimited', () => {
    expect(toBytes(0, 'GB')).toBe(0)
    expect(toBytes(undefined, 'GB')).toBe(0)
    expect(toBytes(-3, 'MB')).toBe(0)
  })

  it('levels usage against the warn threshold', () => {
    expect(usageLevel(10, 80)).toBe('ok')
    expect(usageLevel(80, 80)).toBe('warn')
    expect(usageLevel(99.9, 80)).toBe('warn')
    expect(usageLevel(100, 80)).toBe('full')
    expect(usageLevel(150, 0)).toBe('full')
    expect(usageLevel(95, 0)).toBe('ok')
    expect(usageLevel(95, undefined)).toBe('ok')
    expect(usageBarClass(90, 80)).toBe('bg-warning')
    expect(usageBarClass(100, 80)).toBe('bg-danger')
    expect(usageBarClass(1, 80)).toBe('bg-success')
  })

  it('computes a percentage only when a limit is set', () => {
    expect(percentOf(50, 200)).toBe(25)
    expect(percentOf(undefined, 200)).toBe(0)
    expect(percentOf(50, 0)).toBe(0)
    expect(percentOf(50, undefined)).toBe(0)
  })
})
