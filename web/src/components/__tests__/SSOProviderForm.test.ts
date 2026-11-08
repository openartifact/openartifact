// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import SSOProviderForm from '../../pages/settings/sections/SSOProviderForm.vue'
import { emptyForm } from '../../lib/ssoProviderForm'

function mountForm() {
  const form = emptyForm()
  const wrapper = mount(SSOProviderForm, {
    props: { editing: null, modelValue: form, 'onUpdate:modelValue': () => {} },
  })
  return { form, wrapper }
}

describe('SSOProviderForm Dex preset', () => {
  it('hides the connector field until Dex is selected, then shows it and hides it again', async () => {
    const { form, wrapper } = mountForm()
    expect(wrapper.find('#oidc-dex_connector_id').exists()).toBe(false)
    await wrapper.get('#oidc-type').setValue('dex')
    expect(form.oidc_config.dex).toBe(true)
    expect(form.oidc_config.type).toBe('')
    const input = wrapper.get('#oidc-dex_connector_id')
    await input.setValue('github-main')
    expect(form.oidc_config.dex_connector_id).toBe('github-main')
    await wrapper.get('#oidc-type').setValue('github')
    expect(form.oidc_config.dex_connector_id).toBe('')
    expect(wrapper.find('#oidc-dex_connector_id').exists()).toBe(false)
  })
})
