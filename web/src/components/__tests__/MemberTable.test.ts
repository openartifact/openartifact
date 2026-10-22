// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type DOMWrapper } from '@vue/test-utils'
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
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async () =>
    status >= 400 ? json({ error: 'x' }, status) : json({}),
  )
  return mount(MemberTable, { props: { project, members, groups }, global: { plugins: [createPinia()] } })
}

const disabledOptions = (select: DOMWrapper<Element>) =>
  select.findAll('option').filter((o) => (o.element as HTMLOptionElement).disabled).map((o) => o.attributes('value'))

describe('MemberTable escalation rules', () => {
  it('lets an owner grant every member role and edit every row', () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' })
    const add = wrapper.find('[data-testid="member-add"] select')
    expect(add.findAll('option').map((o) => o.attributes('value'))).toEqual(['viewer', 'developer', 'maintainer', 'owner'])
    expect(disabledOptions(add)).toEqual([])
    const rows = wrapper.findAll('[data-testid="member-row"]')
    // u1 is the recorded owner: protected; the others are editable.
    expect((rows[0]!.find('select').element as HTMLSelectElement).disabled).toBe(true)
    expect((rows[1]!.find('select').element as HTMLSelectElement).disabled).toBe(false)
    expect((rows[2]!.find('select').element as HTMLSelectElement).disabled).toBe(false)
  })

  it('disables the maintainer and owner roles for a maintainer, and locks their rows', () => {
    const wrapper = mountTable({ name: 'p', my_role: 'maintainer', owner_id: 'u1' })
    const add = wrapper.find('[data-testid="member-add"] select')
    expect(disabledOptions(add)).toEqual(['maintainer', 'owner'])

    const rows = wrapper.findAll('[data-testid="member-row"]')
    const locked = (i: number) => (rows[i]!.find('select').element as HTMLSelectElement).disabled
    expect(rows.map((_, i) => locked(i))).toEqual([true, true, true, false, false])

    // A developer row stays editable, but the caller still cannot pick maintainer/owner.
    expect(disabledOptions(rows[3]!.find('select'))).toEqual(['maintainer', 'owner'])
    const removeButtons = rows.map((r) => (r.find('button').element as HTMLButtonElement).disabled)
    expect(removeButtons).toEqual([true, true, true, false, false])

    const groupAdd = wrapper.find('[data-testid="group-attach"] select')
    expect(groupAdd.findAll('option').map((o) => o.attributes('value'))).toEqual(['viewer', 'developer', 'maintainer'])
    expect(disabledOptions(groupAdd)).toEqual(['maintainer'])
    const groupRows = wrapper.findAll('[data-testid="group-row"]')
    expect((groupRows[0]!.find('select').element as HTMLSelectElement).disabled).toBe(true)
    expect((groupRows[1]!.find('select').element as HTMLSelectElement).disabled).toBe(false)
  })

  it('protects the last owner even when the caller is an owner', () => {
    const members: ProjectMember[] = [
      { user_id: 'u9', username: 'solo', role: 'owner' },
      { user_id: 'u4', username: 'dev', role: 'developer' },
    ]
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'someone-else' }, members)
    const rows = wrapper.findAll('[data-testid="member-row"]')
    expect((rows[0]!.find('select').element as HTMLSelectElement).disabled).toBe(true)
    expect((rows[0]!.find('button').element as HTMLButtonElement).disabled).toBe(true)
    expect((rows[1]!.find('select').element as HTMLSelectElement).disabled).toBe(false)
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

  it('adds a member by username with the chosen role and asks the page to reload', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'maintainer', owner_id: 'u1' })
    await wrapper.find('#member-username').setValue('newbie')
    await wrapper.find('[data-testid="member-add"] select').setValue('developer')
    await wrapper.find('[data-testid="member-add"]').trigger('submit')
    await flushPromises()
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe('/api/v1/projects/p/members')
    expect((init as RequestInit).method).toBe('POST')
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ username: 'newbie', role: 'developer' })
    expect(wrapper.emitted('changed')).toHaveLength(1)
  })

  it('changes a member role with PATCH', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' })
    const row = wrapper.findAll('[data-testid="member-row"]')[3]!
    await row.find('select').setValue('maintainer')
    await flushPromises()
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe('/api/v1/projects/p/members/u4')
    expect((init as RequestInit).method).toBe('PATCH')
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ role: 'maintainer' })
  })

  it('attaches a group by name', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' })
    await wrapper.find('#group-name').setValue('release team')
    await wrapper.find('[data-testid="group-attach"] select').setValue('maintainer')
    await wrapper.find('[data-testid="group-attach"]').trigger('submit')
    await flushPromises()
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe('/api/v1/projects/p/groups/release%20team')
    expect((init as RequestInit).method).toBe('PUT')
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ role: 'maintainer' })
  })

  it('still asks the page to reload when a change is rejected', async () => {
    const wrapper = mountTable({ name: 'p', my_role: 'owner', owner_id: 'u1' }, MEMBERS, GROUPS, 409)
    await wrapper.findAll('[data-testid="member-row"]')[3]!.find('select').setValue('viewer')
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
