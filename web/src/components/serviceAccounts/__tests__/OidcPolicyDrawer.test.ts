// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import OidcPolicyDrawer from '../OidcPolicyDrawer.vue'
import type { OidcPolicy } from '../../../api/serviceAccounts'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const ACCOUNTS = [
  { id: 'sa1', name: 'ci-bot', description: '', disabled: false },
  { id: 'sa2', name: 'deployer', description: '', disabled: false },
]

let fetchSpy: Mock<typeof fetch>
let reply: () => Response

beforeEach(() => {
  reply = () => json({ id: 'p1' }, 201)
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async () => reply()) as never)
})
afterEach(() => {
  fetchSpy.mockRestore()
  document.body.innerHTML = ''
})

const $ = <T extends Element>(sel: string): T => document.querySelector<T>(sel)!
async function setValue(sel: string, value: string): Promise<void> {
  const el = $<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(sel)
  el.value = value
  el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input'))
  await flushPromises()
}
const click = async (sel: string): Promise<void> => {
  $<HTMLElement>(sel).click()
  await flushPromises()
}
const submit = async (): Promise<void> => {
  $<HTMLFormElement>('#oidc-policy-form').dispatchEvent(new Event('submit', { cancelable: true }))
  await flushPromises()
}
const sent = () => JSON.parse(String(fetchSpy.mock.calls.at(-1)![1]!.body))

const mountDrawer = async (props: Record<string, unknown> = {}) => {
  const wrapper = mount(OidcPolicyDrawer, { props: { open: true, policy: null, accounts: ACCOUNTS, ...props }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

describe('OidcPolicyDrawer', () => {
  it('the GitHub preset fills the issuer and the identity claim row, and warns that the issuer is shared', async () => {
    const w = await mountDrawer()
    await click('[data-testid=preset-github]')
    expect($<HTMLInputElement>('#policy-issuer').value).toBe('https://token.actions.githubusercontent.com')
    expect(document.querySelectorAll('[data-testid=conditions-editor] textarea')).toHaveLength(1)
    expect(document.body.textContent).toContain('keep a condition on the repository or project')
    w.unmount()
  })

  it('shows what is wrong before sending anything', async () => {
    const w = await mountDrawer()
    await submit()
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('Use 1 to 64 letters, digits, dots, dashes or underscores.')
    w.unmount()
  })

  it('posts the form as the API body, TTL in seconds', async () => {
    const w = await mountDrawer({ accountId: 'sa1' })
    await click('[data-testid=preset-github]')
    await setValue('#policy-name', 'shop')
    await setValue('#policy-audience', 'https://oa.example')
    await setValue('#policy-ttl', '10')
    await setValue('#cond-patterns-0', 'acme/shop')
    await submit()
    expect(sent()).toMatchObject({
      name: 'shop',
      issuer: 'https://token.actions.githubusercontent.com',
      audience: 'https://oa.example',
      service_account_id: 'sa1',
      token_ttl_seconds: 600,
      enabled: true,
    })
    expect(sent().conditions[0]).toEqual({ claim: 'repository', patterns: ['acme/shop'] })
    expect(w.emitted('saved')).toHaveLength(1)
    w.unmount()
  })

  it('shows the message the server rejects the policy with and stays open', async () => {
    reply = () => json({ error: 'issuer is shared: pin the repository' }, 400)
    const w = await mountDrawer({ accountId: 'sa1' })
    await click('[data-testid=preset-gitlab]')
    await setValue('#policy-name', 'shop')
    await setValue('#policy-audience', 'aud')
    await setValue('#cond-patterns-0', '*')
    await submit()
    expect($('[data-testid=policy-error]').textContent).toContain('pin the repository')
    expect(w.emitted('saved')).toBeUndefined()
    w.unmount()
  })

  it('edits an existing policy with PATCH and hides the presets', async () => {
    const policy = {
      id: 'p1',
      name: 'shop',
      issuer: 'https://idp.example',
      audience: 'aud',
      conditions: [{ claim: 'sub', patterns: ['repo:acme/*'] }],
      service_account_id: 'sa2',
      service_account: 'deployer',
      token_ttl_seconds: 900,
      enabled: true,
    } as OidcPolicy
    const w = await mountDrawer({ policy })
    expect(document.querySelector('[data-testid=policy-presets]')).toBeNull()
    expect($<HTMLInputElement>('#policy-ttl').value).toBe('15')
    await submit()
    const [url, init] = fetchSpy.mock.calls.at(-1)!
    expect(String(url)).toBe('/api/v1/oidc-trust-policies/p1')
    expect(init?.method).toBe('PATCH')
    w.unmount()
  })
})
