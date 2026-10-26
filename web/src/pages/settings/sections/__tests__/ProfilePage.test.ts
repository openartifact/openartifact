// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ProfilePage from '../ProfilePage.vue'
import { useSessionStore } from '../../../../stores/session'

beforeEach(() => setActivePinia(createPinia()))

describe('ProfilePage', () => {
  it('leads to the password page and to the personal access tokens', async () => {
    const session = useSessionStore()
    session.user = { id: 'u1', username: 'alice', email: 'alice@example.com' } as never
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
    await router.push('/settings/profile')
    const w = mount(ProfilePage, { global: { plugins: [router] } })

    const block = w.find('[data-testid="profile-credentials"]')
    expect(block.attributes('aria-labelledby')).toBe('profile-credentials-title')
    expect(block.findAll('a').map((a) => a.attributes('href'))).toEqual(['/settings/password', '/settings/tokens?type=personal'])
    expect(w.text()).toContain('alice@example.com')
  })
})
