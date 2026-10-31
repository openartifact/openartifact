// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { defineComponent, h, ref, type Ref } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RepoDefaultsRows from '../RepoDefaultsRows.vue'
import { emptyRepoForm, type RepoForm } from '../../../lib/repoForm'
import type { RepoPolicy } from '../../../lib/projectPolicy'
import { useSessionStore, type User } from '../../../stores/session'

interface Opts {
  mode?: 'create' | 'edit'
  isAdmin?: boolean
  policy?: RepoPolicy | null
  candidates?: string[]
  form?: Partial<RepoForm>
}

async function mountRows(opts: Opts = {}): Promise<{ wrapper: VueWrapper; form: Ref<RepoForm>; projects: unknown[] }> {
  const projects: unknown[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    projects.push(String(input))
    return new Response(JSON.stringify({ projects: [], total: 0 }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch)
  const pinia = createPinia()
  setActivePinia(pinia)
  useSessionStore().user = { id: 'u', username: 'ada', is_admin: opts.isAdmin ?? false } as User
  const form = ref(emptyRepoForm(opts.form))
  const Host = defineComponent({
    setup: () => () =>
      h(RepoDefaultsRows, {
        modelValue: form.value,
        'onUpdate:modelValue': (v: RepoForm) => (form.value = v),
        mode: opts.mode ?? 'create',
        isAdmin: opts.isAdmin ?? false,
        policy: opts.policy ?? null,
        candidates: opts.candidates ?? [],
      }),
  })
  const wrapper = mount(Host, { global: { plugins: [pinia] } })
  await flushPromises()
  return { wrapper, form, projects }
}

const row = (w: VueWrapper, id: string) => w.find(`[data-row="${id}"]`)
const summary = (w: VueWrapper, id: string) => row(w, id).find('[data-testid="row-summary"]').text()
const typeButtons = (w: VueWrapper) => w.findAll('[role="radio"]')
const isOpen = (w: VueWrapper, id: string) => row(w, id).find('button[aria-expanded]').attributes('aria-expanded') === 'true'

describe('RepoDefaultsRows: fields per repository type', () => {
  it('shows the upstream row for proxies and the members row for groups only', async () => {
    const { wrapper, form } = await mountRows()
    expect(row(wrapper, 'upstream').exists()).toBe(false)
    expect(row(wrapper, 'members').exists()).toBe(false)

    await typeButtons(wrapper)[1].trigger('click')
    expect(form.value.type).toBe('proxy')
    expect(row(wrapper, 'upstream').exists()).toBe(true)
    expect(row(wrapper, 'members').exists()).toBe(false)

    await typeButtons(wrapper)[2].trigger('click')
    expect(form.value.type).toBe('group')
    expect(row(wrapper, 'upstream').exists()).toBe(false)
    expect(row(wrapper, 'members').exists()).toBe(true)
  })

  it('locks type selection in edit mode and drops the quota field', async () => {
    const { wrapper } = await mountRows({ mode: 'edit' })
    expect(typeButtons(wrapper)).toHaveLength(0)
    expect(wrapper.find('#rl-quota').exists()).toBe(false)
    expect(wrapper.find('#rl-presign').exists()).toBe(true)
  })

  it('offers the quota and no presign override on create', async () => {
    const { wrapper } = await mountRows()
    expect(wrapper.find('#rl-quota').exists()).toBe(true)
    expect(wrapper.find('#rl-presign').exists()).toBe(false)
  })

  it('disables the types a project policy forbids', async () => {
    const { wrapper, form } = await mountRows({ policy: { allowed_repo_types: ['hosted', 'group'], allow_public_repos: true } as RepoPolicy })
    const disabled = typeButtons(wrapper).map((b) => (b.element as HTMLButtonElement).disabled)
    expect(disabled).toEqual([false, true, false])
    await typeButtons(wrapper)[1].trigger('click')
    expect(form.value.type).toBe('hosted')
  })

  it('locks the public toggle when the project forbids public repositories', async () => {
    const blocked = await mountRows({ policy: { allow_public_repos: false } as RepoPolicy })
    expect((blocked.wrapper.find('[data-row="vis"] input[type="checkbox"]').element as HTMLInputElement).disabled).toBe(true)
    const open = await mountRows({ policy: { allow_public_repos: true } as RepoPolicy })
    expect((open.wrapper.find('[data-row="vis"] input[type="checkbox"]').element as HTMLInputElement).disabled).toBe(false)
    const none = await mountRows()
    expect((none.wrapper.find('[data-row="vis"] input[type="checkbox"]').element as HTMLInputElement).disabled).toBe(false)
  })
})

describe('RepoDefaultsRows: upstream', () => {
  it('opens the upstream row on create when a proxy of a format without a default upstream is chosen', async () => {
    const { wrapper } = await mountRows({ form: { format: 'maven' } })
    expect(row(wrapper, 'upstream').exists()).toBe(false)
    await typeButtons(wrapper)[1].trigger('click')
    expect(isOpen(wrapper, 'upstream')).toBe(true)
    expect(summary(wrapper, 'upstream').toLowerCase()).toContain('required')
  })

  it('keeps it collapsed for formats that default to a public registry, and says so', async () => {
    const { wrapper } = await mountRows({ form: { format: 'pypi' } })
    await typeButtons(wrapper)[1].trigger('click')
    expect(isOpen(wrapper, 'upstream')).toBe(false)
    expect(summary(wrapper, 'upstream').toLowerCase()).not.toContain('required')
    expect((wrapper.find('#rl-upstream-url').element as HTMLInputElement).placeholder).toBe('https://pypi.org/simple')
  })

  it('does not force the row open when a URL is already there', async () => {
    const withUrl = await mountRows({ form: { format: 'maven', upstreamUrl: 'https://repo.example.org' } })
    await typeButtons(withUrl.wrapper)[1].trigger('click')
    expect(isOpen(withUrl.wrapper, 'upstream')).toBe(false)
    expect(summary(withUrl.wrapper, 'upstream')).toBe('https://repo.example.org')
  })

  it('summarises the URL with the username on create', async () => {
    const create = await mountRows({ form: { type: 'proxy', format: 'maven', upstreamUrl: ' https://r.example.org ', upstreamUsername: 'svc' } })
    expect(summary(create.wrapper, 'upstream')).toBe('https://r.example.org · svc')
  })

  it('has no upstream row in edit mode: a proxy\'s upstream is edited on the proxy settings card', async () => {
    const edit = await mountRows({ mode: 'edit', form: { format: 'maven', type: 'proxy' } })
    expect(row(edit.wrapper, 'upstream').exists()).toBe(false)
  })

  it('writes typed upstream values to the form', async () => {
    const { wrapper, form } = await mountRows({ form: { type: 'proxy', format: 'maven' } })
    await wrapper.find('#rl-upstream-url').setValue('https://r.example.org')
    await wrapper.find('#rl-upstream-user').setValue('svc')
    await wrapper.find('#rl-upstream-pass').setValue('pw')
    expect(form.value).toMatchObject({ upstreamUrl: 'https://r.example.org', upstreamUsername: 'svc', upstreamPassword: 'pw' })
  })
})

describe('RepoDefaultsRows: policies and summaries', () => {
  it('offers package signing only for hosted rpm', async () => {
    const count = async (form: Partial<RepoForm>) => (await mountRows({ form })).wrapper.findAll('[data-row="policies"] input[type="checkbox"]').length
    expect(await count({ type: 'hosted', format: 'rpm' })).toBe(2) // sign, immutable
    expect(await count({ type: 'hosted', format: 'deb' })).toBe(1) // immutable
    expect(await count({ type: 'proxy', format: 'rpm' })).toBe(1)
  })

  it('offers federation on create for hosted repositories of capable formats only', async () => {
    const federated = async (form: Partial<RepoForm>, mode: 'create' | 'edit' = 'create') =>
      (await mountRows({ form, mode })).wrapper.text().includes('Federat')
    expect(await federated({ type: 'hosted', format: 'npm' })).toBe(true)
    expect(await federated({ type: 'hosted', format: 'rpm' })).toBe(false)
    expect(await federated({ type: 'proxy', format: 'npm' })).toBe(false)
    expect(await federated({ type: 'group', format: 'npm' })).toBe(false)
    expect(await federated({ type: 'hosted', format: 'npm' }, 'edit')).toBe(false)
  })

  it('summarises policies live from the form', async () => {
    const { wrapper } = await mountRows({ form: { type: 'hosted', format: 'npm' } })
    const before = summary(wrapper, 'policies')
    await wrapper.find('[data-row="policies"] input[type="checkbox"]').setValue(true)
    expect(summary(wrapper, 'policies')).not.toBe(before)
  })

  it('summarises retention and quota on create, retention only on edit', async () => {
    const create = await mountRows({ form: { retentionDays: '30', quotaValue: '5', quotaUnit: 'GB' } })
    expect(summary(create.wrapper, 'retention')).toContain('30')
    expect(summary(create.wrapper, 'retention')).toContain('5.0 GB')
    const edit = await mountRows({ mode: 'edit', form: { retentionDays: '30', quotaValue: '5', quotaUnit: 'GB' } })
    expect(summary(edit.wrapper, 'retention')).toContain('30')
    expect(summary(edit.wrapper, 'retention')).not.toContain('GB')
  })

  it('flags a non-integer retention and accepts blank and zero', async () => {
    const bad = await mountRows({ form: { retentionDays: '2.5' } })
    expect(bad.wrapper.find('#rl-retention').element.closest('div')!.parentElement!.querySelector('[role="alert"]')).not.toBeNull()
    for (const ok of ['', '0', '14']) {
      const w = await mountRows({ form: { retentionDays: ok } })
      expect(w.wrapper.find('#rl-retention').element.closest('div')!.parentElement!.querySelector('[role="alert"]')).toBeNull()
    }
  })

  it('counts members in the members row summary', async () => {
    const { wrapper } = await mountRows({ form: { type: 'group', members: ['a', 'b'] }, candidates: ['a', 'b', 'c'] })
    expect(summary(wrapper, 'members')).toContain('2')
    const none = await mountRows({ form: { type: 'group' } })
    expect(summary(none.wrapper, 'members')).not.toContain('0')
  })

  it('lets only admins pick the project on edit, everyone on create', async () => {
    expect((await mountRows({ mode: 'edit', isAdmin: true })).wrapper.find('#rl-project').exists()).toBe(true)
    expect((await mountRows({ mode: 'edit', isAdmin: false })).wrapper.find('#rl-project').exists()).toBe(false)
    expect((await mountRows({ mode: 'create', isAdmin: false })).wrapper.find('#rl-project').exists()).toBe(true)
  })

  it('lets only admins change merge-proxy-versions on a group', async () => {
    const member = await mountRows({ form: { type: 'group' }, isAdmin: false })
    const boxes = member.wrapper.findAll('[data-row="policies"] input[type="checkbox"]')
    expect((boxes[boxes.length - 1].element as HTMLInputElement).disabled).toBe(true)
    const admin = await mountRows({ form: { type: 'group' }, isAdmin: true })
    const aboxes = admin.wrapper.findAll('[data-row="policies"] input[type="checkbox"]')
    expect((aboxes[aboxes.length - 1].element as HTMLInputElement).disabled).toBe(false)
  })
})
