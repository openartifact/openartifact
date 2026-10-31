// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Component } from 'vue'
import {
  ArrowRightOnRectangleIcon,
  ClipboardDocumentIcon,
  Cog6ToothIcon,
  CubeIcon,
  DocumentIcon,
  FolderIcon,
  HomeIcon,
  LanguageIcon,
  MagnifyingGlassIcon,
  PlusIcon,
  ShieldExclamationIcon,
  SunIcon,
  TagIcon,
  TicketIcon,
  UserIcon,
  UserPlusIcon,
} from '@heroicons/vue/24/outline'
import type { NavIcon } from '../lib/navigation'
import type { PaletteIcon } from '../lib/palette'

const NAV_ICONS: Record<NavIcon, Component> = {
  home: HomeIcon,
  repos: CubeIcon,
  packages: MagnifyingGlassIcon,
  projects: FolderIcon,
  bundles: TagIcon,
  security: ShieldExclamationIcon,
  settings: Cog6ToothIcon,
}

export function navIcon(icon: NavIcon): Component {
  return NAV_ICONS[icon]
}

const PALETTE_ICONS: Record<Exclude<PaletteIcon, NavIcon>, Component> = {
  plus: PlusIcon,
  ticket: TicketIcon,
  'user-plus': UserPlusIcon,
  tag: TagIcon,
  theme: SunIcon,
  'sign-out': ArrowRightOnRectangleIcon,
  clipboard: ClipboardDocumentIcon,
  language: LanguageIcon,
  document: DocumentIcon,
  user: UserIcon,
  search: MagnifyingGlassIcon,
}

export function paletteIcon(icon: PaletteIcon): Component {
  return icon in NAV_ICONS ? NAV_ICONS[icon as NavIcon] : PALETTE_ICONS[icon as Exclude<PaletteIcon, NavIcon>]
}
