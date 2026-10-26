// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { setLocale } from '../../i18n'
import { LOCALE_STORAGE_KEY } from '../../i18n/preference'
import { useLanguage } from '../../composables/useLanguage'
import LanguageSelect from '../LanguageSelect.vue'

beforeEach(() => setActivePinia(createPinia()))
afterEach(async () => {
  await useLanguage().choose('auto')
  localStorage.clear()
  await setLocale('en')
})

describe('LanguageSelect', () => {
  it('offers the browser default and every language by its own name', () => {
    const wrapper = mount(LanguageSelect)
    const options = wrapper.findAll('option').map((o) => o.text())
    expect(options).toEqual(['Browser default', 'English', 'Italiano', 'Deutsch', 'Français', 'Español', 'Português (Brasil)', '简体中文', '日本語'])
  })

  it('applies the chosen language at once, saves it and updates html lang', async () => {
    const wrapper = mount(LanguageSelect, { attachTo: document.body })
    const label = wrapper.get('label').text()
    expect(label).toBe('Language')

    await wrapper.get('select').setValue('de')
    await flushPromises()

    expect(document.documentElement.lang).toBe('de')
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('de')
    expect(wrapper.get('label').text()).not.toBe(label)
    expect((wrapper.get('select').element as HTMLSelectElement).value).toBe('de')
    wrapper.unmount()
  })

  it('forgets the saved choice when set back to the browser default', async () => {
    const wrapper = mount(LanguageSelect, { attachTo: document.body })
    await wrapper.get('select').setValue('ja')
    await flushPromises()
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('ja')
    await wrapper.get('select').setValue('auto')
    await flushPromises()
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull()
    expect(document.documentElement.lang).toBe('en')
    wrapper.unmount()
  })

  it('moves focus to the named element after the switch', async () => {
    const button = document.createElement('button')
    button.id = 'return-target'
    document.body.appendChild(button)
    const wrapper = mount(LanguageSelect, { attachTo: document.body, props: { returnFocusTo: 'return-target' } })
    await wrapper.get('select').setValue('fr')
    await flushPromises()
    expect(document.activeElement).toBe(button)
    button.remove()
    wrapper.unmount()
  })
})
