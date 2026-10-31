// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { registerServiceWorker, retireServiceWorkers, startServiceWorker, startServiceWorkerForServer } from '../register'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n'

class FakeWorker extends EventTarget {
  state = 'installed'
  posted: unknown[] = []
  /** Settles on the first recorded message (the activation request). */
  readonly firstPost = Promise.withResolvers<void>()
  constructor(public precached: string[] = []) {
    super()
  }
  postMessage(msg: unknown, transfer?: MessagePort[]) {
    const m = msg as { type?: string; url?: string }
    // Answers the release query like sw.ts; only the activation request is recorded.
    if (m.type === 'HAS_ASSET') transfer?.[0]?.postMessage(this.precached.includes(m.url ?? ''))
    else {
      this.posted.push(msg)
      this.firstPost.resolve()
    }
  }
}

class FakeRegistration extends EventTarget {
  waiting: FakeWorker | null = null
  installing: FakeWorker | null = null
  update() {
    return Promise.resolve()
  }
}

class FakeContainer extends EventTarget {
  controller: object | null
  registered: Array<[string, unknown]> = []
  constructor(
    public reg: FakeRegistration,
    controlled: boolean,
  ) {
    super()
    this.controller = controlled ? {} : null
  }
  register(url: string, opts: unknown) {
    this.registered.push([url, opts])
    return Promise.resolve(this.reg)
  }
}

const asContainer = (c: FakeContainer) => c as unknown as ServiceWorkerContainer

describe('registerServiceWorker', () => {
  let reg: FakeRegistration
  beforeEach(() => {
    reg = new FakeRegistration()
  })

  it('registers /sw.js with root scope', async () => {
    const c = new FakeContainer(reg, false)
    await registerServiceWorker({ container: asContainer(c) })
    expect(c.registered).toEqual([['/sw.js', { scope: '/' }]])
  })

  it('does nothing without service worker support', async () => {
    expect(await registerServiceWorker({ container: undefined, onUpdate: () => {} })).toBeUndefined()
  })

  it('does not announce the first install', async () => {
    const c = new FakeContainer(reg, false)
    const updates: unknown[] = []
    await registerServiceWorker({ container: asContainer(c), onUpdate: (a) => updates.push(a) })

    const w = new FakeWorker()
    reg.installing = w
    reg.dispatchEvent(new Event('updatefound'))
    w.dispatchEvent(new Event('statechange'))
    expect(updates).toHaveLength(0)
  })

  it('announces an update found while the page runs, once, and applies it on demand', async () => {
    const c = new FakeContainer(reg, true)
    const applies: Array<() => void> = []
    let reloads = 0
    await registerServiceWorker({
      container: asContainer(c),
      reload: () => reloads++,
      onUpdate: (apply) => applies.push(apply),
    })

    const w = new FakeWorker()
    reg.installing = w
    reg.dispatchEvent(new Event('updatefound'))
    w.dispatchEvent(new Event('statechange'))
    w.dispatchEvent(new Event('statechange'))
    expect(applies).toHaveLength(1)

    // Nothing activates or reloads on its own.
    expect(w.posted).toEqual([])
    c.dispatchEvent(new Event('controllerchange'))
    expect(reloads).toBe(0)

    applies[0]()
    expect(w.posted).toEqual([{ type: 'SKIP_WAITING' }])
    expect(reloads).toBe(0)

    c.dispatchEvent(new Event('controllerchange'))
    expect(reloads).toBe(1)
  })

  it('announces a worker that was already waiting at registration', async () => {
    reg.waiting = new FakeWorker()
    const c = new FakeContainer(reg, true)
    const applies: Array<() => void> = []
    await registerServiceWorker({ container: asContainer(c), onUpdate: (apply) => applies.push(apply) })
    expect(applies).toHaveLength(1)
  })

  it('applies to the worker that is waiting now when a newer release replaced the offered one', async () => {
    const c = new FakeContainer(reg, true)
    const applies: Array<() => void> = []
    await registerServiceWorker({ container: asContainer(c), onUpdate: (apply) => applies.push(apply) })

    const first = new FakeWorker()
    reg.installing = first
    reg.dispatchEvent(new Event('updatefound'))
    first.dispatchEvent(new Event('statechange'))

    const second = new FakeWorker()
    reg.waiting = second
    reg.installing = second
    reg.dispatchEvent(new Event('updatefound'))
    second.dispatchEvent(new Event('statechange'))
    expect(applies).toHaveLength(2)

    applies[0]()
    expect(first.posted).toEqual([])
    expect(second.posted).toEqual([{ type: 'SKIP_WAITING' }])
  })

  it('activates quietly, without an offer, a worker of the release already on screen', async () => {
    const c = new FakeContainer(reg, true)
    const applies: Array<() => void> = []
    let reloads = 0
    reg.waiting = new FakeWorker(['/assets/index-new.js'])
    await registerServiceWorker({
      container: asContainer(c),
      pageAsset: '/assets/index-new.js',
      reload: () => reloads++,
      onUpdate: (apply) => applies.push(apply),
    })
    await reg.waiting.firstPost.promise

    expect(applies).toHaveLength(0)
    expect(reg.waiting.posted).toEqual([{ type: 'SKIP_WAITING' }])
    c.dispatchEvent(new Event('controllerchange'))
    expect(reloads).toBe(0)
  })

  it('offers the reload when the page runs an older release than the waiting worker', async () => {
    const c = new FakeContainer(reg, true)
    const offered = Promise.withResolvers<void>()
    reg.waiting = new FakeWorker(['/assets/index-new.js'])
    await registerServiceWorker({
      container: asContainer(c),
      pageAsset: '/assets/index-old.js',
      onUpdate: () => offered.resolve(),
    })

    await offered.promise
    expect(reg.waiting.posted).toEqual([])
  })

  it('reloads at once when another tab already activated the offered worker', async () => {
    const c = new FakeContainer(reg, true)
    const applies: Array<() => void> = []
    let reloads = 0
    const w = new FakeWorker()
    reg.waiting = w
    await registerServiceWorker({ container: asContainer(c), reload: () => reloads++, onUpdate: (a) => applies.push(a) })

    // The other tab's Reload activated it: no waiting worker is left and no controllerchange will follow here.
    w.state = 'activated'
    reg.waiting = null
    c.dispatchEvent(new Event('controllerchange'))
    expect(reloads).toBe(0)

    applies[0]()
    expect(reloads).toBe(1)
    expect(w.posted).toEqual([])
  })

  it('survives a failed registration', async () => {
    const c = new FakeContainer(reg, true)
    c.register = () => Promise.reject(new Error('insecure context'))
    expect(await registerServiceWorker({ container: asContainer(c) })).toBeUndefined()
  })
})

describe('startServiceWorker update toast', () => {
  it('keeps a single standing offer when a newer release arrives', async () => {
    setActivePinia(createPinia())
    const reg = new FakeRegistration()
    reg.waiting = new FakeWorker()
    const c = new FakeContainer(reg, true)
    Object.defineProperty(navigator, 'serviceWorker', { value: c, configurable: true })
    try {
      startServiceWorker()
      await new Promise((r) => setTimeout(r, 0))

      const next = new FakeWorker()
      reg.installing = next
      reg.dispatchEvent(new Event('updatefound'))
      next.dispatchEvent(new Event('statechange'))

      expect(useToastStore().queue).toHaveLength(1)
    } finally {
      Reflect.deleteProperty(navigator, 'serviceWorker')
    }
  })

  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('pushes a sticky "New version available" toast whose Reload action applies the update', async () => {
    const reg = new FakeRegistration()
    reg.waiting = new FakeWorker()
    const c = new FakeContainer(reg, true)
    Object.defineProperty(navigator, 'serviceWorker', { value: c, configurable: true })
    try {
      startServiceWorker()
      await new Promise((r) => setTimeout(r, 0))

      const toasts = useToastStore()
      expect(toasts.queue).toHaveLength(1)
      const toast = toasts.queue[0]
      expect(toast.title).toBe(t('pwa.updateAvailable'))
      expect(toast.action?.label).toBe(t('pwa.reload'))
      expect(toast.timeoutMs).toBe(0)

      toast.action?.run()
      expect(reg.waiting.posted).toEqual([{ type: 'SKIP_WAITING' }])
    } finally {
      Reflect.deleteProperty(navigator, 'serviceWorker')
    }
  })
})

describe('startServiceWorkerForServer', () => {
  function install(registrations: Array<{ unregister: () => Promise<boolean> }>) {
    const reg = new FakeRegistration()
    const c = new FakeContainer(reg, false) as FakeContainer & { getRegistrations?: () => Promise<unknown[]> }
    c.getRegistrations = () => Promise.resolve(registrations)
    Object.defineProperty(navigator, 'serviceWorker', { value: c, configurable: true })
    return c
  }
  const settle = () => new Promise((r) => setTimeout(r, 0))

  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('registers the worker of a release build', async () => {
    const c = install([])
    try {
      await startServiceWorkerForServer(() => Promise.resolve('0.15.1'))
      await settle()
      expect(c.registered).toEqual([['/sw.js', { scope: '/' }]])
    } finally {
      Reflect.deleteProperty(navigator, 'serviceWorker')
    }
  })

  it('registers no worker for a development server and removes one left by an earlier visit', async () => {
    let unregistered = 0
    const c = install([{ unregister: () => Promise.resolve(++unregistered > 0) }])
    try {
      await startServiceWorkerForServer(() => Promise.resolve('dev'))
      await settle()
      expect(c.registered).toEqual([])
      expect(unregistered).toBe(1)
    } finally {
      Reflect.deleteProperty(navigator, 'serviceWorker')
    }
  })

  it('treats a server that does not say its version as a release', async () => {
    const c = install([])
    try {
      await startServiceWorkerForServer(() => Promise.resolve(null))
      await settle()
      expect(c.registered).toHaveLength(1)
    } finally {
      Reflect.deleteProperty(navigator, 'serviceWorker')
    }
  })
})

describe('retireServiceWorkers', () => {
  it('does nothing without service worker support, and survives a failing unregister', async () => {
    await retireServiceWorkers(undefined)
    const failing = { getRegistrations: () => Promise.resolve([{ unregister: () => Promise.reject(new Error('busy')) }]) }
    await retireServiceWorkers(failing as unknown as ServiceWorkerContainer)
  })
})
