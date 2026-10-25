// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Component } from 'vue'
import { t, type TranslationKey } from '../i18n/en'

export type SettingsGroupId =
  | 'account'
  | 'access'
  | 'storage'
  | 'security'
  | 'integrations'
  | 'appearance'
  | 'system'

export type SettingsSectionId =
  | 'profile'
  | 'password'
  | 'tokens'
  | 'users'
  | 'groups'
  | 'permissions'
  | 'sso'
  | 'project-policy'
  | 'backends'
  | 'tiers'
  | 'quotas'
  | 'cleanup'
  | 'trash'
  | 'backup'
  | 'scanners'
  | 'policies'
  | 'signing'
  | 'dependency-track'
  | 'webhooks'
  | 'federation'
  | 'ai'
  | 'branding'
  | 'audit'
  | 'health'

type Loader = () => Promise<{ default: Component }>

export interface SettingsSection {
  id: SettingsSectionId
  group: SettingsGroupId
  titleKey: TranslationKey
  descKey: TranslationKey
  /** Search terms of the hub (matched next to the title and description). */
  keywords: readonly string[]
  /** Only global administrators open it; everybody else is sent to the dashboard. */
  adminOnly: boolean
  /** Page component. Sections without a page yet point at the placeholder; the slice that builds the page swaps the loader. */
  load: Loader
  /** Page for administrators when it differs from `load` (the personal view is the default). */
  loadAdmin?: Loader
}

export const SETTINGS_GROUPS: readonly { id: SettingsGroupId; titleKey: TranslationKey; descKey: TranslationKey }[] = [
  { id: 'account', titleKey: 'ui.settings.group.account', descKey: 'ui.settings.group.account.desc' },
  { id: 'access', titleKey: 'ui.settings.group.access', descKey: 'ui.settings.group.access.desc' },
  { id: 'storage', titleKey: 'ui.settings.group.storage', descKey: 'ui.settings.group.storage.desc' },
  { id: 'security', titleKey: 'ui.settings.group.security', descKey: 'ui.settings.group.security.desc' },
  { id: 'integrations', titleKey: 'ui.settings.group.integrations', descKey: 'ui.settings.group.integrations.desc' },
  { id: 'appearance', titleKey: 'ui.settings.group.appearance', descKey: 'ui.settings.group.appearance.desc' },
  { id: 'system', titleKey: 'ui.settings.group.system', descKey: 'ui.settings.group.system.desc' },
]

const placeholder: Loader = () => import('../pages/settings/SettingsPlaceholder.vue')

function section(
  id: SettingsSectionId,
  group: SettingsGroupId,
  adminOnly: boolean,
  load: Loader,
  keywords: readonly string[],
  loadAdmin?: Loader,
): SettingsSection {
  return {
    id,
    group,
    adminOnly,
    load,
    loadAdmin,
    keywords,
    titleKey: `ui.settings.${id}.title` as TranslationKey,
    descKey: `ui.settings.${id}.desc` as TranslationKey,
  }
}

/** Every settings section, in hub order. */
export const SETTINGS_SECTIONS: readonly SettingsSection[] = [
  section('profile', 'account', false, () => import('../pages/settings/sections/ProfilePage.vue'), ['username', 'email', 'groups', 'display name']),
  section('password', 'account', false, () => import('../pages/settings/sections/PasswordPage.vue'), ['change password', 'credentials']),
  section(
    'tokens',
    'access',
    false,
    () => import('../pages/settings/TokensPage.vue'),
    ['personal', 'ci', 'deploy', 'service', 'api key', 'project token', 'federation token', 'expiry', 'rotate', 'revoke'],
    () => import('../pages/admin/TokensPage.vue'),
  ),
  section('users', 'access', true, () => import('../pages/settings/sections/UsersPage.vue'), ['sign-in', 'disable', 'invite', 'email', 'password reset', 'create user']),
  section('groups', 'access', true, () => import('../pages/settings/sections/GroupsPage.vue'), ['members', 'team']),
  section('permissions', 'access', true, () => import('../pages/settings/sections/PermissionsPage.vue'), ['acl', 'roles', 'targets']),
  section('sso', 'access', true, () => import('../pages/settings/sections/SSOPage.vue'), ['sso', 'single sign-on', 'oidc', 'google', 'okta', 'login provider', 'saml', 'issuer', 'client id']),
  section('project-policy', 'access', true, placeholder, ['project creation', 'self-create', 'allowed formats', 'default quota']),
  section('backends', 'storage', true, () => import('../pages/settings/sections/StorageBackendsPage.vue'), ['s3 bucket', 'filesystem', 'gcs', 'azure', 'endpoint', 'credentials', 'region']),
  section('tiers', 'storage', true, () => import('../pages/settings/sections/StorageTiersPage.vue'), ['cold', 'hot', 'mover', 'placement', 'age', 'policy']),
  section('quotas', 'storage', true, placeholder, ['limit', 'usage', 'over quota', 'size']),
  section('cleanup', 'storage', true, placeholder, ['retention', 'keep last', 'age', 'prune', 'dry run']),
  section('trash', 'storage', true, () => import('../pages/settings/sections/TrashSection.vue'), ['restore', 'purge', 'retention', 'deleted', 'recover']),
  section('backup', 'storage', true, () => import('../pages/settings/sections/BackupPage.vue'), ['snapshot', 'restore', 'schedule', 'download']),
  section('scanners', 'security', true, placeholder, ['trivy', 'osv', 'scan schedule', 'severity threshold', 'auto scan']),
  section('policies', 'security', true, placeholder, ['license policy', 'quarantine', 'block', 'deny list', 'allow list', 'copyleft']),
  section('signing', 'security', true, () => import('../pages/settings/sections/GpgKeysPage.vue'), ['gpg', 'key', 'sign packages', 'public key', 'fingerprint']),
  section('dependency-track', 'security', true, () => import('../pages/settings/sections/DependencyTrackPage.vue'), ['dt', 'sbom push', 'api key', 'url', 'project']),
  section('webhooks', 'integrations', true, () => import('../pages/settings/sections/WebhooksPage.vue'), ['events', 'secret', 'delivery', 'url', 'payload']),
  section('federation', 'integrations', true, placeholder, ['peers', 'replicate', 'sync', 'inbound token']),
  section('ai', 'integrations', true, () => import('../pages/settings/sections/AISettingsPage.vue'), ['llm', 'provider', 'model', 'api key', 'openai', 'anthropic', 'endpoint']),
  section('branding', 'appearance', true, () => import('../pages/settings/sections/BrandingPage.vue'), ['logo', 'colour', 'color', 'accent', 'product name', 'login message', 'favicon', 'white label', 'support url']),
  section('audit', 'system', true, () => import('../pages/settings/sections/AuditLogPage.vue'), ['events', 'export', 'retention', 'actor', 'csv', 'http requests']),
  section('health', 'system', true, () => import('../pages/settings/sections/SystemPage.vue'), ['version', 'database', 'uptime', 'status', 'build', 'commit']),
]

const BY_ID = new Map<string, SettingsSection>(SETTINGS_SECTIONS.map((s) => [s.id, s]))

/** The section registered under `id`, or undefined for an unknown id. */
export function findSettingsSection(id: unknown): SettingsSection | undefined {
  return typeof id === 'string' ? BY_ID.get(id) : undefined
}

/** Display title of a section id (the id itself when unknown, so a crumb never renders empty). */
export function settingsSectionTitle(id: unknown): string {
  const s = findSettingsSection(id)
  return s ? t(s.titleKey) : typeof id === 'string' ? id : t('ui.settings.hub')
}
