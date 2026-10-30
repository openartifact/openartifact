// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import PasswordPage from '../PasswordPage.vue'
import { useToastStore } from '../../../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const LONG = 'correct-horse-battery'
const OLD = 'old-password-value'

function mountPage(change: () => Response | Promise<Response> = () => new Response(null, { status: 204 })) {
  setActivePinia(createPinia())
  const spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) =>
    String(input).includes('/auth/change-password') ? change() : json({})) as never)
  const w = mount(PasswordPage, { attachTo: document.body })
  return { w, spy, toasts: useToastStore() }
}

async function fill(w: ReturnType<typeof mount>, cur: string, next: string, confirm: string) {
  await w.get('#current_password').setValue(cur)
  await w.get('#new_password').setValue(next)
  await w.get('#confirm_password').setValue(confirm)
  await w.get('form').trigger('submit')
  await flushPromises()
}

const calls = (spy: ReturnType<typeof spyOn>) =>
  spy.mock.calls.filter((c) => String(c[0]).includes('/auth/change-password'))

describe('PasswordPage', () => {
  it('names every field with a label and ties an error to its field', async () => {
    const { w } = mountPage()
    for (const id of ['current_password', 'new_password', 'confirm_password']) {
      expect(w.find(`label[for="${id}"]`).exists()).toBe(true)
      expect(w.get(`#${id}`).attributes('aria-invalid')).toBe('false')
    }
    await fill(w, OLD, 'a'.repeat(11), 'a'.repeat(11))
    const bad = w.get('[aria-invalid="true"]')
    const err = w.get(`#${bad.attributes('aria-describedby')?.split(' ').find((i) => i.endsWith('_error'))}`)
    expect(err.classes()).toContain('text-danger')
    w.unmount()
  })

  const cases: [string, string, string, string][] = [
    ['too short', OLD, 'a'.repeat(11), 'a'.repeat(11)],
    ['mismatched confirmation', OLD, LONG, LONG + '!'],
    ['unchanged password', LONG, LONG, LONG],
  ]
  for (const [name, cur, next, confirm] of cases) {
    it(`rejects ${name} without calling the server`, async () => {
      const { w, spy, toasts } = mountPage()
      await fill(w, cur, next, confirm)
      expect(calls(spy)).toHaveLength(0)
      expect(w.findAll('p.text-danger')).toHaveLength(1)
      expect(toasts.queue).toHaveLength(0)
      w.unmount()
    })
  }

  it('on success posts a JSON body, toasts success, clears all fields', async () => {
    const { w, spy, toasts } = mountPage()
    await fill(w, OLD, LONG, LONG)
    const [url, init] = calls(spy)[0] as [string, RequestInit]
    expect(url).not.toContain(LONG)
    expect(JSON.parse(String(init.body))).toEqual({ current_password: OLD, new_password: LONG })
    expect(toasts.queue.map((x) => x.level)).toEqual(['success'])
    for (const id of ['#current_password', '#new_password', '#confirm_password']) {
      expect((w.get(id).element as HTMLInputElement).value).toBe('')
    }
    expect(w.findAll('p.text-danger')).toHaveLength(0)
    w.unmount()
  })

  it('on a wrong current password toasts the server error, keeps input, leaks no secret', async () => {
    const { w, toasts } = mountPage(() => json({ error: 'current password is incorrect' }, 401))
    await fill(w, OLD, LONG, LONG)
    expect(toasts.queue).toHaveLength(1)
    expect(toasts.queue[0]!.level).toBe('error')
    expect(toasts.queue[0]!.title).toBe('current password is incorrect')
    expect(JSON.stringify(toasts.queue)).not.toContain(LONG)
    expect(JSON.stringify(toasts.queue)).not.toContain(OLD)
    expect((w.get('#current_password').element as HTMLInputElement).value).toBe(OLD)
    w.unmount()
  })

  it('on a network failure toasts an error and re-enables submit', async () => {
    const { w, toasts } = mountPage(() => Promise.reject(new TypeError('Failed to fetch')))
    await fill(w, OLD, LONG, LONG)
    expect(toasts.queue[0]!.level).toBe('error')
    expect(w.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    w.unmount()
  })

  it('disables submit while in flight and sends one request', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { w, spy } = mountPage(async () => {
      await gate
      return new Response(null, { status: 204 })
    })
    await w.get('#current_password').setValue(OLD)
    await w.get('#new_password').setValue(LONG)
    await w.get('#confirm_password').setValue(LONG)
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    release()
    await flushPromises()
    expect(calls(spy)).toHaveLength(1)
    w.unmount()
  })
})
