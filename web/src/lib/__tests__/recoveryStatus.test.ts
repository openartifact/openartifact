// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { backlogCapped, recoveryHealth } from '../recoveryStatus'

describe('recoveryHealth', () => {
  it('is off whenever the journal is disabled, whatever else is reported', () => {
    expect(recoveryHealth({ enabled: false, backlog: 5, retrying_rows: 2 })).toBe('off')
    expect(recoveryHealth({})).toBe('off')
  })
  it('is failing when rows are retrying, even with a backlog', () => {
    expect(recoveryHealth({ enabled: true, backlog: 3, retrying_rows: 1 })).toBe('failing')
  })
  it('is catching up with a backlog and nothing retrying', () => {
    expect(recoveryHealth({ enabled: true, backlog: 3, retrying_rows: 0 })).toBe('catching-up')
  })
  it('is healthy with an empty backlog', () => {
    expect(recoveryHealth({ enabled: true, backlog: 0 })).toBe('healthy')
    expect(recoveryHealth({ enabled: true })).toBe('healthy')
  })
})

describe('backlogCapped', () => {
  it('is true only when the backlog reached a positive cap', () => {
    expect(backlogCapped({ backlog: 1000, backlog_cap: 1000 })).toBe(true)
    expect(backlogCapped({ backlog: 999, backlog_cap: 1000 })).toBe(false)
    expect(backlogCapped({ backlog: 10, backlog_cap: 0 })).toBe(false)
    expect(backlogCapped({ backlog: 10 })).toBe(false)
  })
})
