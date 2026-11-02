// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { jest } from 'bun:test'
import { setActivePinia, createPinia } from 'pinia'
import { useToastStore } from '../toast'

describe('useToastStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('push() adds a toast to the queue', () => {
    const store = useToastStore()
    const id = store.push({ level: 'info', title: 'Test Toast' })

    expect(store.queue).toHaveLength(1)
    expect(store.queue[0].id).toBe(id)
    expect(store.queue[0].title).toBe('Test Toast')
    expect(store.queue[0].level).toBe('info')
  })

  it('push() returns the toast id', () => {
    const store = useToastStore()
    const id1 = store.push({ level: 'info', title: 'Toast 1' })
    const id2 = store.push({ level: 'success', title: 'Toast 2' })

    expect(id1).toBe(1)
    expect(id2).toBe(2)
  })

  it('dismiss(id) removes a toast by id', () => {
    const store = useToastStore()
    const id1 = store.push({ level: 'info', title: 'Toast 1' })
    const id2 = store.push({ level: 'success', title: 'Toast 2' })

    expect(store.queue).toHaveLength(2)

    store.dismiss(id1)
    expect(store.queue).toHaveLength(1)
    expect(store.queue[0].id).toBe(id2)
  })

  it('clear() empties the queue', () => {
    const store = useToastStore()
    store.push({ level: 'info', title: 'Toast 1' })
    store.push({ level: 'success', title: 'Toast 2' })
    store.push({ level: 'warning', title: 'Toast 3' })

    expect(store.queue).toHaveLength(3)

    store.clear()
    expect(store.queue).toHaveLength(0)
  })

  it('info() convenience helper calls push with info level', () => {
    const store = useToastStore()
    const id = store.info('Info Title', 'Info Description')

    expect(store.queue).toHaveLength(1)
    expect(store.queue[0].level).toBe('info')
    expect(store.queue[0].title).toBe('Info Title')
    expect(store.queue[0].description).toBe('Info Description')
  })

  it('success() convenience helper calls push with success level', () => {
    const store = useToastStore()
    const id = store.success('Success Title', 'Success Description')

    expect(store.queue).toHaveLength(1)
    expect(store.queue[0].level).toBe('success')
    expect(store.queue[0].title).toBe('Success Title')
    expect(store.queue[0].description).toBe('Success Description')
  })

  it('warning() convenience helper calls push with warning level', () => {
    const store = useToastStore()
    const id = store.warning('Warning Title', 'Warning Description')

    expect(store.queue).toHaveLength(1)
    expect(store.queue[0].level).toBe('warning')
    expect(store.queue[0].title).toBe('Warning Title')
    expect(store.queue[0].description).toBe('Warning Description')
  })

  it('error() convenience helper calls push with error level', () => {
    const store = useToastStore()
    const id = store.error('Error Title', 'Error Description')

    expect(store.queue).toHaveLength(1)
    expect(store.queue[0].level).toBe('error')
    expect(store.queue[0].title).toBe('Error Title')
    expect(store.queue[0].description).toBe('Error Description')
  })

  it('visible computed returns at most MAX_VISIBLE toasts', () => {
    const store = useToastStore()

    // Add more toasts than MAX_VISIBLE
    for (let i = 0; i < 10; i++) {
      store.push({ level: 'info', title: `Toast ${i}` })
    }

    expect(store.queue).toHaveLength(10)
    expect(store.visible).toHaveLength(store.MAX_VISIBLE)
    expect(store.visible).toHaveLength(5)
  })

  it('visible computed returns toasts in order', () => {
    const store = useToastStore()
    const ids = [
      store.push({ level: 'info', title: 'Toast 1' }),
      store.push({ level: 'info', title: 'Toast 2' }),
      store.push({ level: 'info', title: 'Toast 3' }),
    ]

    expect(store.visible.map((t) => t.id)).toEqual(ids)
  })

  it('auto-dismiss fires after timeout', async () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()
      const id = store.push({ level: 'info', title: 'Auto Dismiss Toast', timeoutMs: 1000 })

      expect(store.queue).toHaveLength(1)

      jest.advanceTimersByTime(1000)
      await Promise.resolve()

      expect(store.queue).toHaveLength(0)
    } finally {
      jest.useRealTimers()
    }
  })

  it('auto-dismiss respects default timeout for each level', async () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()

      // Info has 4000ms default
      const infoId = store.info('Info')
      expect(store.queue).toHaveLength(1)
      jest.advanceTimersByTime(4000)
      await Promise.resolve()
      expect(store.queue).toHaveLength(0)

      // Error has 8000ms default
      const errorId = store.error('Error')
      expect(store.queue).toHaveLength(1)
      jest.advanceTimersByTime(8000)
      await Promise.resolve()
      expect(store.queue).toHaveLength(0)
    } finally {
      jest.useRealTimers()
    }
  })

  it('dismiss() cancels auto-dismiss timer', async () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()
      const id = store.push({ level: 'info', title: 'Toast', timeoutMs: 5000 })

      expect(store.queue).toHaveLength(1)

      // Dismiss before timeout
      store.dismiss(id)
      expect(store.queue).toHaveLength(0)

      // Advance time past the original timeout
      jest.advanceTimersByTime(5000)
      await Promise.resolve()

      // Should still be empty (timer was cancelled)
      expect(store.queue).toHaveLength(0)
    } finally {
      jest.useRealTimers()
    }
  })

  it('clear() cancels all auto-dismiss timers', async () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()
      store.push({ level: 'info', title: 'Toast 1', timeoutMs: 5000 })
      store.push({ level: 'success', title: 'Toast 2', timeoutMs: 5000 })
      store.push({ level: 'warning', title: 'Toast 3', timeoutMs: 5000 })

      expect(store.queue).toHaveLength(3)

      store.clear()
      expect(store.queue).toHaveLength(0)

      // Advance time past the original timeouts
      jest.advanceTimersByTime(5000)
      await Promise.resolve()

      // Should still be empty (all timers were cancelled)
      expect(store.queue).toHaveLength(0)
    } finally {
      jest.useRealTimers()
    }
  })

  it('handles toasts with timeoutMs of 0 (no auto-dismiss)', async () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()
      const id = store.push({ level: 'info', title: 'Persistent Toast', timeoutMs: 0 })

      expect(store.queue).toHaveLength(1)

      // Advance time significantly
      jest.advanceTimersByTime(10000)
      await Promise.resolve()

      // Toast should still be there
      expect(store.queue).toHaveLength(1)
      expect(store.queue[0].id).toBe(id)
    } finally {
      jest.useRealTimers()
    }
  })

  // WCAG 2.2.1: a keyboard or pointer user who is on a toast must not lose it under their hands.
  it('pause() stops the countdown and resume() restarts it in full', () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()
      const id = store.push({ level: 'info', title: 'Hold me', timeoutMs: 4000 })
      jest.advanceTimersByTime(3000)
      store.pause(id)
      jest.advanceTimersByTime(60_000)
      expect(store.queue).toHaveLength(1)

      store.resume(id)
      jest.advanceTimersByTime(3999)
      expect(store.queue).toHaveLength(1)
      jest.advanceTimersByTime(1)
      expect(store.queue).toHaveLength(0)
    } finally {
      jest.useRealTimers()
    }
  })

  it('resume() of a toast that is gone or already counting does nothing', () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()
      const id = store.push({ level: 'info', title: 'Counting', timeoutMs: 4000 })
      store.resume(id)
      store.resume(9999)
      jest.advanceTimersByTime(4000)
      expect(store.queue).toHaveLength(0)
    } finally {
      jest.useRealTimers()
    }
  })

  it('keeps a toast with a button (Undo) up for at least 12 s, so the keyboard can reach it', () => {
    jest.useFakeTimers()
    try {
      const store = useToastStore()
      store.push({ level: 'success', title: 'Quarantined', action: { label: 'Undo', run: () => {} } })
      jest.advanceTimersByTime(11_999)
      expect(store.queue).toHaveLength(1)
      jest.advanceTimersByTime(1)
      expect(store.queue).toHaveLength(0)
      // A sticky toast stays sticky, a plain one keeps its level default.
      const sticky = store.push({ level: 'info', title: 'New version', timeoutMs: 0, action: { label: 'Reload', run: () => {} } })
      expect(store.queue.find((t) => t.id === sticky)?.timeoutMs).toBe(0)
    } finally {
      jest.useRealTimers()
    }
  })
})
