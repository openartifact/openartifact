// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import FormatBadge from '../FormatBadge.vue'
import { formatIcon, ALL_FORMATS } from '../../lib/formats'

describe('FormatBadge', () => {
  it('renders format label', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'docker' } })
    expect(wrapper.text()).toContain('Docker')
  })

  it('renders with accent bar', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'npm' } })
    // Check for the accent bar element (span with style)
    const bar = wrapper.find('span[style]')
    expect(bar.exists()).toBe(true)
  })

  it('handles unknown format gracefully', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'unknown' } })
    expect(wrapper.text()).toContain('unknown')
  })

  it('applies size prop', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'pypi', size: 'sm' } })
    expect(wrapper.exists()).toBe(true)
  })

  it('renders correct label for npm', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'npm' } })
    expect(wrapper.text()).toContain('npm')
  })

  it('renders correct label for pypi', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'pypi' } })
    expect(wrapper.text()).toContain('PyPI')
  })

  it('renders correct label for maven', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'maven' } })
    expect(wrapper.text()).toContain('Maven')
  })

  it('renders correct label for helm', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'helm' } })
    expect(wrapper.text()).toContain('Helm')
  })

  it('renders correct label for cargo', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'cargo' } })
    expect(wrapper.text()).toContain('Cargo')
  })

  it('renders correct label for pacman', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'pacman' } })
    expect(wrapper.text()).toContain('Arch Linux (pacman)')
  })

  it('renders the label for nix', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'nix' } })
    expect(wrapper.text()).toContain('Nix')
  })

  it('renders the Alpine label and icon for apk', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'apk' } })
    expect(wrapper.text()).toContain('Alpine (apk)')
    expect(wrapper.find('svg path').exists()).toBe(true)
  })

  it('renders correct label for ansible', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'ansible' } })
    expect(wrapper.text()).toContain('Ansible')
  })

  it('renders correct label for terraform', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'terraform' } })
    expect(wrapper.text()).toContain('Terraform')
  })

  it('renders correct label for rubygems', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'rubygems' } })
    expect(wrapper.text()).toContain('RubyGems')
  })

  it('renders correct label for deb', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'deb' } })
    expect(wrapper.text()).toContain('Deb')
  })

  it('renders correct label for rpm', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'rpm' } })
    expect(wrapper.text()).toContain('RPM')
  })

  it('renders correct label for nuget', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'nuget' } })
    expect(wrapper.text()).toContain('NuGet')
  })

  it('renders correct label for go', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'go' } })
    expect(wrapper.text()).toContain('Go')
  })

  it('renders correct label for raw', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'raw' } })
    expect(wrapper.text()).toContain('Raw')
  })

  it('renders docker brand icon svg path', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'docker' } })
    const path = wrapper.find('svg path')
    expect(path.exists()).toBe(true)
    expect(path.attributes('d')).toBe(formatIcon('docker'))
  })

  it('renders raw format with the generic archive icon', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'raw' } })
    expect(wrapper.find('svg path').attributes('d')).toBe(formatIcon('raw'))
  })

  it('every known format has an icon', () => {
    for (const id of ALL_FORMATS) expect(formatIcon(id)).toBeTruthy()
  })

  it('renders fallback color bar for unknown formats', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'mystery' } })
    expect(wrapper.find('span.self-stretch').exists()).toBe(true)
    expect(wrapper.find('svg path').exists()).toBe(false)
  })

  it('case-insensitive format matching', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'DOCKER' } })
    expect(wrapper.text()).toContain('Docker')
  })

  it('renders span with correct classes', () => {
    const wrapper = mount(FormatBadge, { props: { format: 'docker' } })
    const span = wrapper.find('span')
    expect(span.classes()).toContain('inline-flex')
    expect(span.classes()).toContain('items-center')
  })
})
