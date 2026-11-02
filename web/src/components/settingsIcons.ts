// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Component } from 'vue'
import {
  ArchiveBoxIcon,
  ArrowsUpDownIcon,
  BoltIcon,
  ChartPieIcon,
  ClipboardDocumentListIcon,
  CodeBracketSquareIcon,
  CpuChipIcon,
  DocumentCheckIcon,
  FingerPrintIcon,
  FolderIcon,
  FunnelIcon,
  GlobeAltIcon,
  HeartIcon,
  KeyIcon,
  LifebuoyIcon,
  LockClosedIcon,
  MagnifyingGlassCircleIcon,
  PaintBrushIcon,
  ScaleIcon,
  ScissorsIcon,
  ServerStackIcon,
  ShieldCheckIcon,
  ShieldExclamationIcon,
  SparklesIcon,
  TicketIcon,
  TrashIcon,
  UserCircleIcon,
  UserGroupIcon,
  UsersIcon,
} from '@heroicons/vue/24/outline'
import type { SettingsSectionId } from '../lib/settingsSections'

const ICONS: Record<SettingsSectionId, Component> = {
  profile: UserCircleIcon,
  password: LockClosedIcon,
  tokens: TicketIcon,
  users: UsersIcon,
  'service-accounts': CpuChipIcon,
  groups: UserGroupIcon,
  permissions: ShieldCheckIcon,
  sso: KeyIcon,
  'project-policy': FolderIcon,
  backends: ServerStackIcon,
  tiers: ArrowsUpDownIcon,
  quotas: ChartPieIcon,
  cleanup: ScissorsIcon,
  trash: TrashIcon,
  backup: ArchiveBoxIcon,
  recovery: LifebuoyIcon,
  scanners: MagnifyingGlassCircleIcon,
  policies: ScaleIcon,
  signing: FingerPrintIcon,
  'dependency-track': ShieldExclamationIcon,
  attestation: DocumentCheckIcon,
  'scan-gate': FunnelIcon,
  webhooks: BoltIcon,
  federation: GlobeAltIcon,
  ai: SparklesIcon,
  branding: PaintBrushIcon,
  audit: ClipboardDocumentListIcon,
  declarative: CodeBracketSquareIcon,
  health: HeartIcon,
}

export function settingsIcon(id: SettingsSectionId): Component {
  return ICONS[id]
}
