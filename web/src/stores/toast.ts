// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export type ToastLevel = 'info' | 'success' | 'warning' | 'error'

/** A button on the toast; running it dismisses the toast. */
export interface ToastAction {
  label: string
  run: () => void
}

export interface Toast {
  id: number
  level: ToastLevel
  title: string
  description?: string
  action?: ToastAction
  timeoutMs: number
  createdAt: number
}

const DEFAULT_TIMEOUT_MS: Record<ToastLevel, number> = {
  info: 4000,
  success: 4000,
  warning: 6000,
  error: 8000,
}

const MAX_VISIBLE = 5

/** A toast with a button (Undo) stays at least this long: a keyboard user needs time to reach it. */
const ACTION_MIN_TIMEOUT_MS = 12000

/** A toast that stays until dismissed (the "new version" offer): it is docked in the page, not floated over it. */
export function isSticky(toast: Toast): boolean {
  return toast.timeoutMs === 0
}

export const useToastStore = defineStore('toast', () => {
  const queue = ref<Toast[]>([])
  let nextId = 1
  const timers = new Map<number, ReturnType<typeof setTimeout>>()

  const visible = computed<Toast[]>(() => queue.value.slice(0, MAX_VISIBLE))

  function push(input: Omit<Toast, 'id' | 'createdAt' | 'timeoutMs'> & { timeoutMs?: number }): number {
    const id = nextId++
    let timeoutMs = input.timeoutMs ?? DEFAULT_TIMEOUT_MS[input.level]
    if (input.action && timeoutMs > 0) timeoutMs = Math.max(timeoutMs, ACTION_MIN_TIMEOUT_MS)
    const toast: Toast = {
      id,
      title: input.title,
      description: input.description,
      action: input.action,
      level: input.level,
      timeoutMs,
      createdAt: Date.now(),
    }
    queue.value.push(toast)
    startTimer(toast)
    return id
  }

  function startTimer(toast: Toast): void {
    if (toast.timeoutMs <= 0) return
    timers.set(toast.id, setTimeout(() => dismiss(toast.id), toast.timeoutMs))
  }

  /** Stops the countdown while the pointer or keyboard focus is on the toast (WCAG 2.2.1, 1.4.13): it restarts in full on `resume`. */
  function pause(id: number): void {
    const handle = timers.get(id)
    if (handle == null) return
    clearTimeout(handle)
    timers.delete(id)
  }

  function resume(id: number): void {
    const toast = queue.value.find((t) => t.id === id)
    if (toast && !timers.has(id)) startTimer(toast)
  }

  function dismiss(id: number): void {
    const idx = queue.value.findIndex((t) => t.id === id)
    if (idx !== -1) queue.value.splice(idx, 1)
    const handle = timers.get(id)
    if (handle != null) {
      clearTimeout(handle)
      timers.delete(id)
    }
  }

  function clear(): void {
    for (const handle of timers.values()) clearTimeout(handle)
    timers.clear()
    queue.value = []
  }

  // Convenience helpers
  function info(title: string, description?: string): number {
    return push({ level: 'info', title, description })
  }
  function success(title: string, description?: string): number {
    return push({ level: 'success', title, description })
  }
  function warning(title: string, description?: string): number {
    return push({ level: 'warning', title, description })
  }
  function error(title: string, description?: string): number {
    return push({ level: 'error', title, description })
  }

  return { queue, visible, push, dismiss, pause, resume, clear, info, success, warning, error, MAX_VISIBLE }
})
