// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import PackageUploadZone from '../PackageUploadZone.vue'
import { useToastStore } from '../../stores/toast'
import { tarEntry, tgz } from '../../lib/__tests__/tarFixture'

type Listener = () => void

/** Just enough of XMLHttpRequest to drive an upload by hand. */
class FakeXHR {
  static all: FakeXHR[] = []
  status = 0
  responseText = ''
  method = ''
  url = ''
  aborted = false
  body: FormData | Blob | null = null
  headers: Record<string, string> = {}
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
  setRequestHeader(name: string, value: string): void {
    this.headers[name] = value
  }
  addEventListener(name: string, fn: Listener): void {
    this.listeners[name] = fn
  }
  send(body: FormData | Blob): void {
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

/** The multipart body of a request. */
const form = (x: FakeXHR) => x.body as FormData

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
    const wrapper = mountZone({ repo: 'my repo/x', format: 'deb' })
    await pick(wrapper, new File(['a'], 'a.deb'), new File(['b'], 'b.deb'))
    expect(FakeXHR.all).toHaveLength(2)
    for (const x of FakeXHR.all) {
      expect(x.method).toBe('POST')
      expect(x.url).toBe('/api/v1/repositories/my%20repo%2Fx/packages')
    }
    expect((form(FakeXHR.all[0]).get('file') as File).name).toBe('a.deb')
    expect((form(FakeXHR.all[1]).get('file') as File).name).toBe('b.deb')
    expect(rows(wrapper).map((r) => r.find('p').text())).toEqual(['a.deb', 'b.deb'])
  })

  it('renders nothing for a format that cannot be uploaded from a browser', () => {
    for (const format of ['cargo', 'conan', 'docker', 'ivy', 'sbt', undefined]) {
      expect(mountZone({ repo: 'r', format }).find('input[type="file"]').exists()).toBe(false)
    }
  })

  it('reflects upload progress in the entry bar and keeps going files independent', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    await pick(wrapper, new File(['aaaa'], 'a.deb'), new File(['bbbb'], 'b.deb'))
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

  // The bar is a coloured div: without a role and a value a screen reader cannot tell a stalled upload from a running one.
  it('exposes each upload as a named progressbar with its percentage and status', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    await pick(wrapper, new File(['aaaa'], 'a.deb'))
    FakeXHR.all[0].progress(1, 4)
    await flushPromises()
    const bar = rows(wrapper)[0].get('[role="progressbar"]')
    expect(bar.attributes('aria-valuenow')).toBe('25')
    expect(bar.attributes('aria-valuemin')).toBe('0')
    expect(bar.attributes('aria-valuemax')).toBe('100')
    expect(bar.attributes('aria-valuetext')).toContain('25%')
    const name = rows(wrapper)[0].get(`#${bar.attributes('aria-labelledby')}`)
    expect(name.text()).toBe('a.deb')
    // The cancel button says which file it cancels.
    expect(rows(wrapper)[0].get('button').attributes('aria-describedby')).toBe(bar.attributes('aria-labelledby'))
  })

  it('on success marks the entry complete, toasts the file name and emits uploaded once per file', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    await pick(wrapper, new File(['a'], 'a.deb'), new File(['b'], 'b.deb'))
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
    expect(useToastStore().queue.filter((t) => t.level === 'success').map((t) => t.description)).toEqual(['a.deb', 'b.deb'])
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

  it('shows the nix copy instructions instead of a dropzone for nix', () => {
    const wrapper = mountZone({ repo: 'cache', format: 'nix' })
    expect(wrapper.text()).toContain('nix copy --to')
    expect(wrapper.text()).toContain('/repos/cache')
    expect(wrapper.find('input[type="file"]').exists()).toBe(false)
  })

  describe('apk', () => {
    const branch = (w: VueWrapper) => w.find('#upload-branch')
    const repoInput = (w: VueWrapper) => w.find('#upload-repo')

    it('defaults to edge/main and posts file, branch and repo to the apks endpoint', async () => {
      const wrapper = mountZone({ repo: 'my repo', format: 'apk' })
      expect((branch(wrapper).element as HTMLInputElement).value).toBe('edge')
      expect((repoInput(wrapper).element as HTMLInputElement).value).toBe('main')
      await pick(wrapper, new File(['a'], 'a-1.0-r0.apk'))
      expect(FakeXHR.all).toHaveLength(1)
      expect(FakeXHR.all[0].method).toBe('POST')
      expect(FakeXHR.all[0].url).toBe('/api/v1/repositories/my%20repo/apks')
      expect((form(FakeXHR.all[0]).get('file') as File).name).toBe('a-1.0-r0.apk')
      expect(form(FakeXHR.all[0]).get('branch')).toBe('edge')
      expect(form(FakeXHR.all[0]).get('repo')).toBe('main')
    })

    it('sends the edited coordinates', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'apk' })
      await branch(wrapper).setValue('v3.20')
      await repoInput(wrapper).setValue('community')
      await pick(wrapper, new File(['a'], 'a.apk'))
      expect(form(FakeXHR.all[0]).get('branch')).toBe('v3.20')
      expect(form(FakeXHR.all[0]).get('repo')).toBe('community')
    })

    it('omits arch when empty and sends it, validated, when given', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'apk' })
      await pick(wrapper, new File(['a'], 'a.apk'))
      expect(form(FakeXHR.all[0]).has('arch')).toBe(false)
      await wrapper.find('#upload-arch').setValue('bad/arch')
      await pick(wrapper, new File(['a'], 'b.apk'))
      expect(FakeXHR.all).toHaveLength(1)
      await wrapper.find('#upload-arch').setValue('x86_64')
      await pick(wrapper, new File(['a'], 'c.apk'))
      expect(form(FakeXHR.all[1]).get('arch')).toBe('x86_64')
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
      expect(mountZone({ repo: 'r', format: 'deb' }).find('[data-testid="upload-fields"]').exists()).toBe(false)
    })
  })

  describe('formats', () => {
    const only = () => FakeXHR.all[0]
    /** Waits until the zone has sent n requests (npm and go read the file first). */
    async function sent(n: number): Promise<void> {
      for (let i = 0; i < 200 && FakeXHR.all.length < n; i++) {
        await new Promise((r) => setTimeout(r, 0))
        await flushPromises()
      }
      expect(FakeXHR.all).toHaveLength(n)
    }
    const pyWheel = 'upl_pkg-0.4.2-py3-none-any.whl'

    it('raw: PUTs the file itself under the chosen directory', async () => {
      const wrapper = mountZone({ repo: 'my repo', format: 'raw' })
      await wrapper.find('#upload-dir').setValue('/releases/1.0/')
      await pick(wrapper, new File(['x'], 'a b.txt'))
      expect(only().method).toBe('PUT')
      expect(only().url).toBe('/repos/raw/my%20repo/releases/1.0/a%20b.txt')
      expect(only().body).toBeInstanceOf(File)
    })

    it('raw: the directory is optional and may not climb out of the repository', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'raw' })
      await pick(wrapper, new File(['x'], 'a.txt'))
      expect(only().url).toBe('/repos/raw/r/a.txt')
      for (const bad of ['a/../b', '..', 'a//b', 'a\\b']) {
        await wrapper.find('#upload-dir').setValue(bad)
        expect(wrapper.find('[role="alert"]').exists()).toBe(true)
        await pick(wrapper, new File(['x'], 'b.txt'))
      }
      expect(FakeXHR.all).toHaveLength(1)
    })

    it('npm: PUTs the publish document built from the tarball to the package route', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'npm' })
      await wrapper.find('#upload-tag').setValue('beta')
      await pick(wrapper, tgz(tarEntry('package/package.json', JSON.stringify({ name: '@acme/widget', version: '2.0.0' }))))
      await sent(1)
      expect(only().method).toBe('PUT')
      expect(only().url).toBe('/repos/r/@acme%2Fwidget')
      const doc = JSON.parse(await (only().body as Blob).text())
      expect(doc['dist-tags']).toEqual({ beta: '2.0.0' })
      expect(Object.keys(doc._attachments)).toEqual(['widget-2.0.0.tgz'])
    })

    it('npm: a tarball without package.json fails on its row and sends nothing', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'npm' })
      await pick(wrapper, new File(['nope'], 'x.tgz'))
      await flushPromises()
      for (let i = 0; i < 50 && !rows(wrapper)[0].text().includes('package.json'); i++) await new Promise((r) => setTimeout(r, 0))
      expect(rows(wrapper)[0].text()).toContain('No package.json found')
      expect(FakeXHR.all).toHaveLength(0)
      expect(rows(wrapper)[0].find('button').exists()).toBe(false)
      expect(useToastStore().queue.map((t) => t.level)).toEqual(['error'])
    })

    it('npm: a tag that looks like a version is refused', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'npm' })
      await wrapper.find('#upload-tag').setValue('1.0.0')
      expect(wrapper.find('[role="alert"]').exists()).toBe(true)
      await wrapper.find('#upload-tag').setValue('next')
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    })

    it('pypi: posts the legacy upload form with the name and version read from the file name', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'pypi' })
      await pick(wrapper, new File(['w'], pyWheel))
      expect(only().method).toBe('POST')
      expect(only().url).toBe('/repos/r/legacy/')
      const f = form(only())
      expect((f.get('content') as File).name).toBe(pyWheel)
      expect(Object.fromEntries([':action', 'name', 'version', 'filetype', 'pyversion', 'protocol_version'].map((k) => [k, f.get(k)]))).toEqual({
        ':action': 'file_upload',
        name: 'upl_pkg',
        version: '0.4.2',
        filetype: 'bdist_wheel',
        pyversion: 'py3',
        protocol_version: '1',
      })
    })

    it('pypi: a file name without a name and version fails on its row', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'pypi' })
      await pick(wrapper, new File(['w'], 'readme.txt'))
      await flushPromises()
      expect(FakeXHR.all).toHaveLength(0)
      expect(rows(wrapper)[0].text()).toContain('Cannot read a package name and version')
    })

    it('maven: PUTs each file under group/artifact/version and checks the file name against the coordinates', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'maven' })
      expect(wrapper.find('button').attributes('disabled')).toBeDefined()
      await wrapper.find('#upload-group').setValue('com.example.app')
      await wrapper.find('#upload-artifact').setValue('my-lib')
      await wrapper.find('#upload-version').setValue('1.0.0')
      expect(wrapper.find('button').attributes('disabled')).toBeUndefined()
      await pick(wrapper, new File(['j'], 'my-lib-1.0.0.jar'), new File(['s'], 'my-lib-1.0.0-sources.jar'), new File(['x'], 'other-1.0.0.jar'), new File(['x'], 'my-lib-1.0.01.jar'))
      await flushPromises()
      expect(FakeXHR.all.map((x) => [x.method, x.url])).toEqual([
        ['PUT', '/repos/maven/r/com/example/app/my-lib/1.0.0/my-lib-1.0.0.jar'],
        ['PUT', '/repos/maven/r/com/example/app/my-lib/1.0.0/my-lib-1.0.0-sources.jar'],
      ])
      expect(rows(wrapper)[2].text()).toContain('must start with the artifact ID and the version')
      expect(rows(wrapper)[3].text()).toContain('must start with the artifact ID and the version')
    })

    it.each([
      ['helm', 'POST', '/repos/r/charts', 'c-1.0.0.tgz'],
      ['rubygems', 'POST', '/repos/r/api/v1/gems', 'g-1.0.gem'],
    ])('%s: sends the file itself as the request body', async (format, method, url, name) => {
      const wrapper = mountZone({ repo: 'r', format })
      await pick(wrapper, new File(['x'], name))
      expect([only().method, only().url]).toEqual([method, url])
      expect(only().body).toBeInstanceOf(File)
    })

    it.each([
      ['nuget', 'PUT', '/repos/r/v3/package', 'package', 'a.1.0.0.nupkg'],
      ['ansible', 'POST', '/repos/r/api/v3/artifacts/collections/', 'file', 'ns-col-1.0.0.tar.gz'],
    ])('%s: sends a multipart form with the file in %s', async (format, method, url, field, name) => {
      const wrapper = mountZone({ repo: 'r', format })
      await pick(wrapper, new File(['x'], name))
      expect([only().method, only().url]).toEqual([method, url])
      expect((form(only()).get(field) as File).name).toBe(name)
    })

    it('conda: posts the file to the platform with its name in the query', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'conda' })
      await wrapper.find('#upload-platform').setValue('linux-64')
      await pick(wrapper, new File(['x'], 'pkg-1.0-0.conda'))
      expect([only().method, only().url]).toEqual(['POST', '/repos/r/linux-64?filename=pkg-1.0-0.conda'])
      expect(only().body).toBeInstanceOf(File)
    })

    it('composer: PUTs the zip to the package and version', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'composer' })
      await wrapper.find('#upload-package').setValue('Bad Name')
      expect(wrapper.find('[role="alert"]').exists()).toBe(true)
      await wrapper.find('#upload-package').setValue('acme/widgets')
      await wrapper.find('#upload-version').setValue('1.0.0')
      await pick(wrapper, new File(['x'], 'acme-widgets-1.0.0.zip'))
      expect([only().method, only().url]).toEqual(['PUT', '/repos/r/packages/upload/acme/widgets/1.0.0'])
    })

    it('terraform: a module goes to its coordinates, a provider to the platform read from its file name', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'terraform' })
      await wrapper.find('#upload-name').setValue('vpc')
      await wrapper.find('#upload-system').setValue('aws')
      await wrapper.find('#upload-version').setValue('1.0.0-rc.1')
      await pick(wrapper, new File(['x'], 'whatever.tar.gz'))
      expect(only().url).toBe('/repos/r/_api/modules/vpc/aws/1.0.0-rc.1')
      await wrapper.find('#upload-kind').setValue('provider')
      expect(wrapper.find('#upload-name').exists()).toBe(false)
      await pick(wrapper, new File(['x'], 'terraform-provider-my_cloud_2.1.0_linux_amd64.zip'), new File(['x'], 'provider.zip'))
      await flushPromises()
      expect(FakeXHR.all[1].url).toBe('/repos/r/_api/providers/my_cloud/2.1.0/linux/amd64')
      expect(FakeXHR.all).toHaveLength(2)
      expect(rows(wrapper)[2].text()).toContain('terraform-provider-TYPE_VERSION_OS_ARCH.zip')
    })

    it('go: reads the module and version off the zip and escapes capitals in the URL', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'go' })
      const header = new Uint8Array(30)
      header.set([0x50, 0x4b, 3, 4])
      const name = new TextEncoder().encode('github.com/Acme/Tool@v1.2.3-RC.1/go.mod')
      header[26] = name.length
      await pick(wrapper, new File([header, name], 'tool.zip'))
      await sent(1)
      expect([only().method, only().url]).toEqual(['PUT', '/repos/r/github.com/!acme/!tool/@v/v1.2.3-!r!c.1'])
    })

    it('go: a file that is not a module zip fails on its row', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'go' })
      await pick(wrapper, new File(['not a zip'], 'tool.zip'))
      for (let i = 0; i < 50 && !rows(wrapper)[0].text().includes('Go module'); i++) await new Promise((r) => setTimeout(r, 0))
      expect(rows(wrapper)[0].text()).toContain('Not a Go module zip')
      expect(FakeXHR.all).toHaveLength(0)
    })

    it('pacman: uploads packages before signatures, one at a time, into the architecture directory', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'pacman' })
      await wrapper.find('#upload-arch').setValue('x86_64_v3')
      await pick(wrapper, new File(['s'], 'tool-1-1-any.pkg.tar.zst.sig'), new File(['p'], 'tool-1-1-any.pkg.tar.zst'))
      await flushPromises()
      expect(FakeXHR.all).toHaveLength(1)
      expect([only().method, only().url]).toEqual(['PUT', '/repos/r/os/x86_64_v3/tool-1-1-any.pkg.tar.zst'])
      only().respond(201)
      await sent(2)
      expect(FakeXHR.all[1].url).toBe('/repos/r/os/x86_64_v3/tool-1-1-any.pkg.tar.zst.sig')
    })

    it('cancelling while the npm document is still being built sends nothing', async () => {
      const wrapper = mountZone({ repo: 'r', format: 'npm' })
      await pick(wrapper, tgz(tarEntry('package/package.json', JSON.stringify({ name: 'w', version: '1.0.0' }))))
      await rows(wrapper)[0].find('button').trigger('click')
      for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 0))
      expect(FakeXHR.all).toHaveLength(0)
      expect(useToastStore().queue).toHaveLength(0)
    })
  })

  it('reports a network failure on the entry', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    await pick(wrapper, new File(['x'], 'a.deb'))
    FakeXHR.all[0].fail()
    await flushPromises()
    expect(rows(wrapper)[0].text()).toContain('network error')
    expect(wrapper.emitted('uploaded')).toBeUndefined()
  })

  it('cancelling an upload aborts it, drops the cancel button and reports no failure', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    await pick(wrapper, new File(['x'], 'a.deb'))
    await rows(wrapper)[0].find('button').trigger('click')
    await flushPromises()
    expect(FakeXHR.all[0].aborted).toBe(true)
    expect(rows(wrapper)[0].find('button').exists()).toBe(false)
    expect(wrapper.emitted('uploaded')).toBeUndefined()
    expect(useToastStore().queue).toHaveLength(0)
  })

  it('aborts uploads still running when the page is left', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    await pick(wrapper, new File(['a'], 'a.deb'), new File(['b'], 'b.deb'))
    FakeXHR.all[0].respond(201)
    await flushPromises()
    wrapper.unmount()
    expect(FakeXHR.all[0].aborted).toBe(false)
    expect(FakeXHR.all[1].aborted).toBe(true)
  })

  it('uploads every file of a drop', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    const drop = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(drop, 'dataTransfer', { value: { files: [new File(['a'], 'a.deb'), new File(['b'], 'b.deb')] } })
    wrapper.element.dispatchEvent(drop)
    await flushPromises()
    expect(FakeXHR.all).toHaveLength(2)
  })

  it('ignores a drop that carries no files', async () => {
    const wrapper = mountZone({ repo: 'r', format: 'deb' })
    const drop = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(drop, 'dataTransfer', { value: null })
    wrapper.element.dispatchEvent(drop)
    await flushPromises()
    expect(FakeXHR.all).toHaveLength(0)
  })
})
