// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type DOMWrapper, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import MemberTable from '../MemberTable.vue'
import RoleSelect from '../RoleSelect.vue'
import type { Project, ProjectGroup, ProjectMember } from '../../api/projects'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const MEMBERS: ProjectMember[] = [
  { user_id: 'u1', username: 'owner-one', role: 'owner' },
  { user_id: 'u2', username: 'owner-two', role: 'owner' },
  { user_id: 'u3', username: 'maint', role: 'maintainer' },
  { user_id: 'u4', username: 'dev', role: 'developer' },
  { user_id: 'u5', username: 'view', role: 'viewer' },
]
const GROUPS: ProjectGroup[] = [
  { group_id: 'g1', group_name: 'leads', role: 'maintainer' },
  { group_id: 'g2', group_name: 'devs', role: 'developer' },
]

let fetchSpy: Mock<typeof fetch>

function mountTable(project: Project, members = MEMBERS, groups = GROUPS, status = 200) {
  document.body.innerHTML = ''
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async () =>
    status >= 400 ? json({ error: 'x' }, status) : json({}),
  )
  return mount(MemberTable, {
    props: { project, members, groups },
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
}

const disabledOptions = (select: DOMWrapper<Element>) =>
  select.findAll('option').filter((o) => (o.element as HTMLOptionElement).disabled).map((o) => o.attributes('value'))

const memberRows = (w: VueWrapper) => w.findAll('table')[0]!.findAll('tbody tr')
const groupRows = (w: VueWrapper) => w.findAll('table')[1]!.findAll('tbody tr')
const isDisabled = (row: DOMWrapper<Element>, tag: 'select' | 'button') => (row.find(tag).element as HTMLSelectElement).disabled

/** The drawer is teleported to the body: its select and options are read from there. */
function drawerSelect(id: string): HTMLSelectElement {
  return document.body.querySelector(`#${id}`) as HTMLSelectElement
}
const optionValues = (select: HTMLSelectElement) => Array.from(select.options).map((o) => o.value)
const disabledValues = (select: HTMLSelectElement) => Array.from(select.options).filter((o) => o.disabled).map((o) => o.value)

async function type(selector: string, value: string): Promise<void> {
  const el = document.body.querySelector(selector) as HTMLInputElement | HTMLSelectElement
  el.value = value
  el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input'))
  await flushPromises()
}

async function submit(formId: string): Promise<void> {
  ;(document.body.querySelector(formId) as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }))
  await flushPromises()
}

describe('MemberTable escalation rules', () => {
  it('lets an owner grant every member role and edit every row', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' })
    await wrapper.get('[data-testid="member-add"]').trigger('click')
    await flushPromises()
    const add = drawerSelect('member-new-role')
    expect(optionValues(add)).toEqual(['viewer', 'developer', 'maintainer', 'owner'])
    expect(disabledValues(add)).toEqual([])
    const rows = memberRows(wrapper)
    // u1 is the recorded owner: protected; the others are editable.
    expect(isDisabled(rows[0]!, 'select')).toBe(true)
    expect(isDisabled(rows[1]!, 'select')).toBe(false)
    expect(isDisabled(rows[2]!, 'select')).toBe(false)
  })

  it('disables the maintainer and owner roles for a maintainer, and locks their rows', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'maintainer', owner_id: 'u1' })
    await wrapper.get('[data-testid="member-add"]').trigger('click')
    await flushPromises()
    expect(disabledValues(drawerSelect('member-new-role'))).toEqual(['maintainer', 'owner'])

    const rows = memberRows(wrapper)
    expect(rows.map((r) => isDisabled(r, 'select'))).toEqual([true, true, true, false, false])

    // A developer row stays editable, but the caller still cannot pick maintainer/owner.
    expect(disabledOptions(rows[3]!.find('select'))).toEqual(['maintainer', 'owner'])
    expect(rows.map((r) => isDisabled(r, 'button'))).toEqual([true, true, true, false, false])

    await wrapper.get('[data-testid="group-attach"]').trigger('click')
    await flushPromises()
    const groupAdd = drawerSelect('group-new-role')
    expect(optionValues(groupAdd)).toEqual(['viewer', 'developer', 'maintainer'])
    expect(disabledValues(groupAdd)).toEqual(['maintainer'])
    const rowsOfGroups = groupRows(wrapper)
    expect(isDisabled(rowsOfGroups[0]!, 'select')).toBe(true)
    expect(isDisabled(rowsOfGroups[1]!, 'select')).toBe(false)
  })

  it('protects the last owner even when the caller is an owner', () => {
    const members: ProjectMember[] = [
      { user_id: 'u9', username: 'solo', role: 'owner' },
      { user_id: 'u4', username: 'dev', role: 'developer' },
    ]
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'someone-else' }, members)
    const rows = memberRows(wrapper)
    expect(isDisabled(rows[0]!, 'select')).toBe(true)
    expect(isDisabled(rows[0]!, 'button')).toBe(true)
    expect(isDisabled(rows[1]!, 'select')).toBe(false)
  })

  it('renders read-only roles for a developer', () => {
    const wrapper = mountTable({ name: 'p', my_role: 'developer', owner_id: 'u1' })
    expect(wrapper.find('[data-testid="member-add"]').exists()).toBe(false)
    expect(wrapper.findAll('select')).toHaveLength(0)
    expect(wrapper.findAll('[data-testid="role-badge"]').length).toBeGreaterThan(0)
  })

  it('hides membership editing on the system project even for an owner', () => {
    const wrapper = mountTable({ name: 'system', my_role: 'owner', is_system: true })
    expect(wrapper.find('[data-testid="member-add"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="group-attach"]').exists()).toBe(false)
    expect(wrapper.findAll('select')).toHaveLength(0)
    expect(wrapper.find('[data-testid="members-system"]').exists()).toBe(true)
  })

  it('adds a member from the drawer with the chosen role and asks the page to reload', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'maintainer', owner_id: 'u1' })
    await wrapper.get('[data-testid="member-add"]').trigger('click')
    await flushPromises()
    await type('#member-username', 'newbie')
    await type('#member-new-role', 'developer')
    await submit('#member-add-form')
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe('/api/v1/projects/p/members')
    expect((init as RequestInit).method).toBe('POST')
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ username: 'newbie', role: 'developer' })
    expect(wrapper.emitted('changed')).toHaveLength(1)
  })

  it('changes a member role with PATCH and no confirmation', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' })
    await memberRows(wrapper)[3]!.find('select').setValue('maintainer')
    await flushPromises()
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe('/api/v1/projects/p/members/u4')
    expect((init as RequestInit).method).toBe('PATCH')
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ role: 'maintainer' })
  })

  it('asks for confirmation before removing a member', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' })
    await memberRows(wrapper)[3]!.find('button').trigger('click')
    await flushPromises()
    expect(fetchSpy.mock.calls).toHaveLength(0)
    expect(document.body.textContent).toContain('Remove this member?')
  })

  it('attaches a group by name from the drawer', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' })
    await wrapper.get('[data-testid="group-attach"]').trigger('click')
    await flushPromises()
    await type('#group-name', 'release team')
    await type('#group-new-role', 'maintainer')
    await submit('#group-attach-form')
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe('/api/v1/projects/p/groups/release%20team')
    expect((init as RequestInit).method).toBe('PUT')
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ role: 'maintainer' })
  })

  it('still asks the page to reload when a change is rejected', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' }, MEMBERS, GROUPS, 409)
    await memberRows(wrapper)[3]!.find('select').setValue('viewer')
    await flushPromises()
    expect(wrapper.emitted('changed')).toHaveLength(1)
  })
})

describe('RoleSelect', () => {
  it('keeps the current value selectable even when it is not grantable', () => {
    const wrapper = mount(RoleSelect, {
      props: { modelValue: 'owner', roles: ['viewer', 'owner'], grantable: ['viewer'], label: 'Role' },
    })
    expect(disabledOptions(wrapper.find('select'))).toEqual([])
  })

  it('disables roles outside grantable and emits the picked role', async () => {
    const wrapper = mount(RoleSelect, {
      props: { modelValue: 'viewer', roles: ['viewer', 'developer', 'owner'], grantable: ['viewer', 'developer'], label: 'Role' },
    })
    expect(disabledOptions(wrapper.find('select'))).toEqual(['owner'])
    await wrapper.find('select').setValue('developer')
    expect(wrapper.emitted('update:modelValue')).toEqual([['developer']])
  })
})
