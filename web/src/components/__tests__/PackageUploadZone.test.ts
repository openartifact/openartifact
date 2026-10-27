// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import PackageUploadZone from '../PackageUploadZone.vue'
import { useToastStore } from '../../stores/toast'

type Listener = () => void

/** Just enough of XMLHttpRequest to drive an upload by hand. */
class FakeXHR {
  static all: FakeXHR[] = []
  status = 0
  responseText = ''
  method = ''
  url = ''
  aborted = false
  body: FormData | null = null
  private listeners: Record<string, Listener> = {}
  private uploadListeners: Record<string, (ev: { lengthComputable: boolean; loaded: number; total: number }) => void> = {}
  upload = {
    addEventListener: (name: string, fn: (ev: { lengthComputable: boolean; loaded: number; total: number }) => void) => {
      this.uploadListeners[name] = fn
    },
  }
  constructor() {
    FakeXHR.all.push(this)
  }
  open(method: string, url: string): void {
    this.method = method
    this.url = url
  }
  setRequestHeader(): void {}
  addEventListener(name: string, fn: Listener): void {
    this.listeners[name] = fn
  }
  send(body: FormData): void {
    this.body = body
  }
  abort(): void {
    this.aborted = true
    this.listeners.abort?.()
  }
  progress(loaded: number, total: number): void {
    this.uploadListeners.progress?.({ lengthComputable: true, loaded, total })
  }
  respond(status: number, body = ''): void {
    this.status = status
    this.responseText = body
    this.listeners.load?.()
  }
  fail(): void {
    this.listeners.error?.()
  }
}

let pinia = createPinia()

beforeEach(() => {
  FakeXHR.all = []
  globalThis.XMLHttpRequest = FakeXHR as unknown as typeof XMLHttpRequest
  pinia = createPinia()
  setActivePinia(pinia)
})

function mountZone(props: { repo: string; format?: string }): VueWrapper {
  return mount(PackageUploadZone, { props, global: { plugins: [pinia] } })
}

async function pick(wrapper: VueWrapper, ...files: File[]): Promise<void> {
  const input = wrapper.find('input[type="file"]')
  Object.defineProperty(input.element, 'files', { value: files, configurable: true })
  await input.trigger('change')
}

const rows = (w: VueWrapper) => w.findAll('li')

describe('PackageUploadZone', () => {
  it('starts one upload per picked file, posting the file to the repository packages endpoint', async () => {
    const wrapper = mountZone({ repo: 'my repo/x', format: 'maven' })
    await pick(wrapper, new File(['a'], 'a.jar'), new File(['b'], 'b.pom'))
    expect(FakeXHR.all).toHaveLength(2)
    for (const x of FakeXHR.all) {
      expect(x.method).toBe('POST')
      expect(x.url).toBe('/api/v1/repositories/my%20repo%2Fx/packages?format=maven')
    }
    expect((FakeXHR.all[0].body!.get('file') as File).name).toBe('a.jar')
    expect((FakeXHR.all[1].body!.get('file') as File).name).toBe('b.pom')
    expect(rows(wrapper).map((r) => r.find('p').text())).toEqual(['a.jar', 'b.pom'])
  })

  it('omits the format query when the repository has no format', async () => {
    const wrapper = mountZone({ repo: 'r' })
    await pick(wrapper, new File(['a'], 'a.bin'))
    expect(FakeXHR.all[0].url).toBe('/api/v1/repositories/r/packages')
  })

  it('reflects upload progress in the entry bar and keeps going files independent', async () => {
    const wrapper = mountZone({ repo: 'r' })
    await pick(wrapper, new File(['aaaa'], 'a.bin'), new File(['bbbb'], 'b.bin'))
    FakeXHR.all[0].progress(1, 4)
    await flushPromises()
    const bar = (i: number) => rows(wrapper)[i].find('div.h-full')
    expect(bar(0).attributes('style')).toContain('width: 25%')
    expect(bar(1).attributes('style')).toContain('width: 0%')
    // Progress beyond the announced total never overflows the bar.
    FakeXHR.all[1].progress(9, 4)
    await flushPromises()
    expect(bar(1).attributes('style')).toContain('width: 100%')
  })

  it('on success marks the entry complete, toasts the file name and emits uploaded once per file', async () => {
    const wrapper = mountZone({ repo: 'r' })
    await pick(wrapper, new File(['a'], 'a.bin'), new File(['b'], 'b.bin'))
    FakeXHR.all[0].respond(201)
    await flushPromises()
    expect(wrapper.emitted('uploaded')).toHaveLength(1)
    expect(rows(wrapper)[0].find('div.h-full').attributes('style')).toContain('width: 100%')
    // A finished entry offers no cancel; the running one still does.
    expect(rows(wrapper)[0].find('button').exists()).toBe(false)
    expect(rows(wrapper)[1].find('button').exists()).toBe(true)
    FakeXHR.all[1].respond(200)
    await flushPromises()
    expect(wrapper.emitted('uploaded')).toHaveLength(2)
    expect(useToastStore().queue.filter((t) => t.level === 'success').map((t) => t.description)).toEqual(['a.bin', 'b.bin'])
  })

  it('surfaces the server error of a rejected file and does not emit uploaded', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    await pick(wrapper, new File(['x'], 'bad.txt'), new File(['y'], 'good.deb'))
    FakeXHR.all[0].respond(400, JSON.stringify({ error: 'not a deb package', code: 'validation_failed' }))
    await flushPromises()
    expect(rows(wrapper)[0].text()).toContain('not a deb package')
    expect(rows(wrapper)[0].find('button').exists()).toBe(false)
    expect(wrapper.emitted('uploaded')).toBeUndefined()
    expect(useToastStore().queue.map((t) => [t.level, t.description])).toEqual([['error', 'bad.txt: not a deb package']])
    // The second file is unaffected by the first one's failure.
    FakeXHR.all[1].respond(201)
    await flushPromises()
    expect(wrapper.emitted('uploaded')).toHaveLength(1)
    expect(rows(wrapper)[1].text()).not.toContain('not a deb package')
  })

  describe('apk', () => {
    const branch = (w: VueWrapper) => w.find('#apk-upload-branch')
    const repoInput = (w: VueWrapper) => w.find('#apk-upload-repo')

    it('defaults to edge/main and posts file, branch and repo to the apks endpoint', async () => {
      const wrapper = mountZone({ repo: 'my repo', format: 'apk' })
      expect((branch(wrapper).element as HTMLInputElement).value).toBe('edge')
      expect((repoInput(wrapper).element as HTMLInputElement).value).toBe('main')
      await pick(wrapper, new File(['a'], 'a-1.0-r0.apk'))
      expect(FakeXHR.all).toHaveLength(1)
      expect(FakeXHR.all[0].method).toBe('POST')
      expect(FakeXHR.all[0].url).toBe('/api/v1/repositories/my%20repo/apks')
      expect((FakeXHR.all[0].body!.get('file') as File).name).toBe('a-1.0-r0.apk')
      expect(FakeXHR.all[0].body!.get('branch')).toBe('edge')
      expect(FakeXHR.all[0].body!.get('repo')).toBe('main')
    })

    it('sends the edited coordinates', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'apk' })
      await branch(wrapper).setValue('v3.20')
      await repoInput(wrapper).setValue('community')
      await pick(wrapper, new File(['a'], 'a.apk'))
      expect(FakeXHR.all[0].body!.get('branch')).toBe('v3.20')
      expect(FakeXHR.all[0].body!.get('repo')).toBe('community')
    })

    it('omits arch when empty and sends it, validated, when given', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'apk' })
      await pick(wrapper, new File(['a'], 'a.apk'))
      expect(FakeXHR.all[0].body!.has('arch')).toBe(false)
      await wrapper.find('#apk-upload-arch').setValue('bad/arch')
      await pick(wrapper, new File(['a'], 'b.apk'))
      expect(FakeXHR.all).toHaveLength(1)
      await wrapper.find('#apk-upload-arch').setValue('x86_64')
      await pick(wrapper, new File(['a'], 'c.apk'))
      expect(FakeXHR.all[1].body!.get('arch')).toBe('x86_64')
    })

    it('rejects invalid segments: shows the error, disables browsing and starts nothing', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'apk' })
      for (const bad of ['', '.', '..', '-x', 'a/b', 'a b', 'a'.repeat(65)]) {
        await branch(wrapper).setValue(bad)
        expect(wrapper.find('[role="alert"]').exists()).toBe(true)
        expect(wrapper.find('button').attributes('disabled')).toBeDefined()
        await pick(wrapper, new File(['a'], 'a.apk'))
        expect(FakeXHR.all).toHaveLength(0)
      }
      await branch(wrapper).setValue('a'.repeat(64))
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    })

    it('does not show the coordinate inputs for other formats', () => {
      expect(mountZone({ repo: 'r', format: 'deb' }).find('[data-testid="apk-coordinates"]').exists()).toBe(false)
    })
  })

  it('reports a network failure on the entry', async () => {
    const wrapper = mountZone({ repo: 'r' })
    await pick(wrapper, new File(['x'], 'a.bin'))
    FakeXHR.all[0].fail()
    await flushPromises()
    expect(rows(wrapper)[0].text()).toContain('network error')
    expect(wrapper.emitted('uploaded')).toBeUndefined()
  })

  it('cancelling an upload aborts it, drops the cancel button and reports no failure', async () => {
    const wrapper = mountZone({ repo: 'r' })
    await pick(wrapper, new File(['x'], 'a.bin'))
    await rows(wrapper)[0].find('button').trigger('click')
    await flushPromises()
    expect(FakeXHR.all[0].aborted).toBe(true)
    expect(rows(wrapper)[0].find('button').exists()).toBe(false)
    expect(wrapper.emitted('uploaded')).toBeUndefined()
    expect(useToastStore().queue).toHaveLength(0)
  })

  it('aborts uploads still running when the page is left', async () => {
    const wrapper = mountZone({ repo: 'r' })
    await pick(wrapper, new File(['a'], 'a.bin'), new File(['b'], 'b.bin'))
    FakeXHR.all[0].respond(201)
    await flushPromises()
    wrapper.unmount()
    expect(FakeXHR.all[0].aborted).toBe(false)
    expect(FakeXHR.all[1].aborted).toBe(true)
  })

  it('uploads every file of a drop', async () => {
    const wrapper = mountZone({ repo: 'r' })
    const drop = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(drop, 'dataTransfer', { value: { files: [new File(['a'], 'a.bin'), new File(['b'], 'b.bin')] } })
    wrapper.element.dispatchEvent(drop)
    await flushPromises()
    expect(FakeXHR.all).toHaveLength(2)
  })

  it('ignores a drop that carries no files', async () => {
    const wrapper = mountZone({ repo: 'r' })
    const drop = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(drop, 'dataTransfer', { value: null })
    wrapper.element.dispatchEvent(drop)
    await flushPromises()
    expect(FakeXHR.all).toHaveLength(0)
  })
})
