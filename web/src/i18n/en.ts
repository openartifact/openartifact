// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Flat English string catalog. `t(key)` is typed against the catalog so a
// missing key is a compile error; use `tDynamic` for runtime-built keys.
// Per-area keys live in en.repos.ts / en.admin.ts / en.auth.ts / en.projects.ts / en.tiering.ts. The intent is to decouple components from literal
// strings so that adding a second locale later is mechanical.

import { enRepos } from './en.repos'
import { enAdmin } from './en.admin'
import { enAuth } from './en.auth'
import { enProjects } from './en.projects'
import { enTiering } from './en.tiering'
import { enBundles } from './en.bundles'
import { enFederation } from './en.federation'
import { enBranding } from './en.branding'

const base = {
  // App / shell

  // Auth
  'auth.signIn': 'Sign in',
  'auth.signOut': 'Sign out',
  'auth.username': 'Username',
  'auth.password': 'Password',
  'auth.currentPassword': 'Current Password',
  'auth.newPassword': 'New Password',
  'auth.confirmPassword': 'Confirm New Password',
  'auth.changePassword': 'Change your password',
  'auth.changePasswordRequired': 'Your administrator requires you to set a new password before continuing.',
  'auth.changePasswordCta': 'Set new password',
  'auth.changePasswordSuccess': 'Password changed successfully',
  'auth.passwordMin': 'Minimum 12 characters.',
  'auth.passwordMinError': 'Password must be at least 12 characters',
  'auth.passwordMismatch': 'Passwords do not match',
  'auth.passwordSame': 'New password must differ from the current one',
  'auth.passwordChangeFailed': 'Failed to change password',
  'auth.loginFailed': 'Login failed',
  'auth.logout': 'Logout',
  'auth.logoutFailed': 'Could not sign out',
  'session.probeFailed': "Couldn't verify your session",
  'session.probeFailedHint': 'The server did not answer. Navigating again retries the check.',

  // Navigation
  'nav.dashboard': 'Dashboard',
  'nav.repositories': 'Repositories',
  'nav.search': 'Search',
  'nav.profile': 'Profile',
  'nav.settings': 'Settings',
  'nav.users': 'Users',
  'nav.groups': 'Groups',
  'nav.permissions': 'Permissions',
  'nav.sso': 'SSO',
  'nav.system': 'System',
  'nav.gpg': 'GPG Keys',
  'nav.tokens': 'Tokens',
  'nav.audit': 'Audit Log',
  'nav.trash': 'Trash',
  'nav.storage': 'Storage Backends',
  'nav.storageTiers': 'Storage Tiers',
  'nav.backup': 'Backup & Restore',
  'nav.webhooks': 'Webhooks',
  'nav.dependencyTrack': 'Dependency-Track',
  'nav.security': 'Security',
  'nav.vulns': 'Vulnerabilities',
  'nav.licenses': 'Licenses',
  'nav.secrets': 'Secrets',

  // Sidebar groups
  'sidebar.browse': 'Browse',
  'sidebar.admin': 'Administration',
  'sidebar.settings': 'Settings',
  'sidebar.collapse': 'Collapse sidebar',
  'sidebar.expand': 'Expand sidebar',
  'sidebar.open': 'Open navigation',
  'sidebar.close': 'Close navigation',

  // Theme
  'theme.light': 'Light',
  'theme.dark': 'Dark',
  'theme.auto': 'System',

  // Common
  'common.justNow': 'just now',
  'common.loading': 'Loading...',
  'common.export.jsonl': 'Export JSONL',
  'common.export.csv': 'Export CSV',
  'common.export.failed': 'Export failed',
  'common.save': 'Save',
  'common.saving': 'Saving...',
  'common.cancel': 'Cancel',
  'common.create': 'Create',
  'common.edit': 'Edit',
  'common.delete': 'Delete',
  'common.confirm': 'Confirm',
  'common.close': 'Close',
  'common.copy': 'Copy',
  'common.copied': 'Copied',
  'common.actions': 'Actions',
  'common.notAvailable': 'Not yet available',
  'common.notAvailableHint': 'This admin endpoint is not yet implemented in this build.',
  'common.viewDocs': 'View documentation',
  'common.optional': 'optional',
  'common.empty': 'Nothing to show',
  'common.refresh': 'Refresh',

  // Pagination
  'pagination.showing': 'Showing',
  'pagination.of': 'of',
  'pagination.previous': 'Previous',
  'pagination.next': 'Next',

  // Dashboard
  'dashboard.title': 'Dashboard',
  'dashboard.recent': 'Recent Repositories',

  // Repositories
  'repos.title': 'Repositories',
  'repos.empty': 'No repositories',
  'repos.emptyHint': 'Create your first repository to get started.',
  'repos.column.name': 'Name',
  'repos.column.format': 'Format',
  'repos.column.visibility': 'Visibility',
  'repos.public': 'Public',
  'repos.private': 'Private',
  'repos.create': 'Create repository',

  // Repo wizard
  'wizard.title': 'New repository',
  'wizard.editTitle': 'Edit repository',
  'wizard.step.format': 'Format',
  'wizard.step.review': 'Review',
  'wizard.public': 'Public repository',
  'wizard.publicHint': 'Public repositories can be browsed without authentication.',
  'wizard.create': 'Create repository',
  'wizard.update': 'Save changes',

  // Packages
  'packages.column.name': 'Name',
  'packages.column.version': 'Version',
  'packages.column.uploaded': 'Uploaded',
  'packages.column.size': 'Size',
  'packages.column.sha': 'SHA-256',

  // Upload
  'upload.dropzone': 'Drag files here or click to browse',
  'upload.browse': 'Browse files',
  'upload.cancel': 'Cancel',
  'upload.uploading': 'Uploading',
  'upload.complete': 'Uploaded',
  'upload.failed': 'Failed',
  'upload.queued': 'Queued',
  'upload.hint.deb': 'Upload .deb packages.',
  'upload.hint.rpm': 'Upload .rpm packages.',
  'upload.hint.maven': 'Upload .jar / .pom artifacts; the path encodes the coordinates.',
  'upload.hint.npm': 'Upload .tgz tarballs published by `npm pack`.',
  'upload.hint.generic': 'Upload package files for this repository.',

  // Search
  'search.title': 'Search',
  'search.empty': 'No results',
  'search.emptyHint': 'Try a different query or relax your filters.',
  'search.placeholder': 'Search packages and repositories...',
  'search.results.packages': 'Packages',
  'search.results.repositories': 'Repositories',

  // Command palette
  'palette.placeholder': 'Type a command, search packages or repositories...',
  'palette.dialogLabel': 'Command palette',
  'palette.resultsLabel': 'Results',
  'palette.noResults': 'No matches',
  'palette.searching': 'Searching...',
  'palette.tooBroad': 'Too many matches. Refine your query, for example with name:, format: or repo:.',
  'palette.error': 'Search failed.',
  'palette.retry': 'Retry',
  'palette.count.one': '1 result',
  'palette.count.other': '{n} results',
  'palette.viewAll': 'View all results for "{q}"',
  'palette.esc': 'Esc',
  'palette.group.commands': 'Commands',
  'palette.group.packages': 'Packages',
  'palette.group.repositories': 'Repositories',
  'palette.command.dashboard': 'Go to dashboard',
  'palette.command.repos': 'Go to repositories',
  'palette.command.search': 'Open search',
  'palette.command.settings': 'Open settings',
  'palette.command.theme.light': 'Switch to light theme',
  'palette.command.theme.dark': 'Switch to dark theme',
  'palette.command.theme.auto': 'Match system theme',
  'palette.command.logout': 'Log out',

  // Toasts

  // Installable app (PWA)
  'pwa.updateAvailable': 'New version available',
  'pwa.updateHint': 'Reload to switch to it. Your current page is left untouched until you do.',
  'pwa.reload': 'Reload',
  'pwa.install': 'Install app',

  // Status bar

  // Admin
  'admin.users.title': 'Users',
  'admin.users.create': 'Create user',
  'admin.users.disable': 'Disable',
  'admin.users.enable': 'Enable',
  'admin.users.forceReset': 'Force password reset',
  'admin.users.column.username': 'Username',
  'admin.users.column.email': 'Email',
  'admin.users.column.created': 'Created',

  'admin.groups.title': 'Groups',
  'admin.groups.create': 'Create group',
  'admin.groups.column.name': 'Name',
  'admin.groups.column.members': 'Members',
  'admin.groups.column.created': 'Created',
  'admin.groups.manageMembers': 'Manage members',

  'admin.permissions.title': 'Permissions',
  'admin.permissions.revoke': 'Revoke',

  'admin.sso.title': 'SSO Providers',
  'admin.sso.description': 'Configure identity providers for federated authentication.',
  'admin.sso.add': 'Add provider',
  'admin.sso.empty': 'No SSO providers configured',

  'admin.system.title': 'System',
  'admin.system.version': 'Version',
  'admin.system.go': 'Go Version',
  'admin.system.uptime': 'Uptime',
  'admin.system.database': 'Database',
  'admin.system.build': 'Build',
  'admin.system.storage': 'Storage',

  'admin.gpg.title': 'GPG Keys',
  'admin.gpg.description': 'Manage GPG signing keys for repository metadata.',
  'admin.gpg.import': 'Import key',
  'admin.gpg.generate': 'Generate key',
  'admin.gpg.column.name': 'Name',
  'admin.gpg.column.keyId': 'Key ID',
  'admin.gpg.column.algorithm': 'Algorithm',
  'admin.gpg.column.source': 'Source',
  'admin.gpg.column.active': 'Active for',
  'admin.gpg.column.created': 'Created',
  'admin.gpg.column.expires': 'Expires',

  'admin.tokens.title': 'API tokens',
  'admin.tokens.revoke': 'Revoke',
  'admin.tokens.column.label': 'Label',
  'admin.tokens.column.created': 'Created',
  'admin.tokens.column.project': 'Project',
  'admin.tokens.column.createdBy': 'Created by',
  'admin.tokens.column.lastUsed': 'Last used',
  'admin.tokens.reveal': 'This token will only be shown once. Copy it now.',

  'admin.audit.title': 'Audit log',
  'admin.audit.column.timestamp': 'When',
  'admin.audit.column.actor': 'Actor',
  'admin.audit.column.action': 'Action',
  'admin.audit.column.target': 'Target',
  'admin.audit.filter.actor': 'Actor',
  'admin.audit.filter.action': 'Action',


  'admin.storage.title': 'Storage backends',

  // Artifact Browser
  'browser.title': 'Browse',
  'browser.filterPlaceholder': 'Filter artifacts…',
  'browser.errorTitle': 'Failed to load artifacts',
  'browser.loadFailed': 'Failed to load artifacts',
  'browser.retry': 'Retry',
  'browser.emptyTitle': 'No artifacts',
  'browser.emptyHint': 'No artifacts have been uploaded to this repository yet.',
  'browser.noMatchTitle': 'No matching artifacts',
  'browser.noMatchHint': 'Try a different search term.',
  'browser.selectAll': 'Select all on page',

  // Dependency Graph
  'graph.title': 'Dependency graph',
  'graph.sbomList': 'Select SBOM',
  'graph.selectSbom': '— choose a SBOM —',
  'graph.loading': 'Loading…',
  'graph.exportSvg': 'Export SVG',
  'graph.emptyTitle': 'No graph loaded',
  'graph.emptyHint': 'Select a SBOM from the list, enter an artifact identifier, or add manual dependencies to visualize the dependency graph.',
  'graph.legend': 'Legend',
  'graph.nodes': 'nodes',
  'graph.edges': 'edges',
  'graph.clickHint': 'Click a node to highlight connections',

  // Bulk Actions
  'bulk.selectedOne': 'item selected',
  'bulk.selectedMany': '{n} items selected',
  'bulk.deleteSelected': 'Delete selected',
  'bulk.clearSelection': 'Clear selection',
  'bulk.confirmDelete': 'Delete {n} artifacts?',
  'bulk.confirmDeleteHint': 'Deleted packages are moved to the repository trash and can be restored until the trash is emptied or the retention period ends.',
  'bulk.deleting': 'Deleting…',
  'bulk.deleteSuccess': 'Deleted {n} artifacts',
  'bulk.deleteFailed': 'Failed to delete some artifacts',


  // Settings
  'settings.profile': 'Profile',
  'settings.password': 'Password',
  'settings.passwordDesc': 'Update your account password. Minimum 12 characters required.',
  'settings.tokens': 'Personal access tokens',
  'settings.tokens.subtitle': 'Tokens grant API access on your behalf. Keep them secret.',
  'settings.tokens.empty': 'You have no active tokens.',
  'settings.tokens.emptyHint': 'Package clients and CI jobs sign in with a token; your account password only works for the web UI.',
  'settings.tokens.create': 'Create token',
  'settings.tokens.label': 'Label',

  // Documentation links
  'docs.helpPage': 'Open the documentation for this page (new tab)',
  'docs.readMore': 'Read the documentation',
  'docs.sso': 'Set up single sign-on',
  'docs.tokens': 'About API tokens',
  'docs.rbac': 'How permission targets work',
  'docs.projects': 'About projects',
  'docs.webhooks': 'Webhook events and signatures',
  'docs.trash': 'How the trash and retention work',
} as const

const catalog = { ...base, ...enRepos, ...enAdmin, ...enAuth, ...enProjects, ...enTiering, ...enBundles, ...enFederation, ...enBranding } satisfies Record<string, string>

export type TranslationKey = keyof typeof catalog

/** Typed lookup: unknown keys are a compile error. */
export function t(key: TranslationKey): string {
  return catalog[key]
}

/**
 * Typed lookup with `{name}` placeholders filled from `params`. Use it for
 * strings that carry the product name or other runtime values, instead of
 * concatenating translated fragments.
 */
export function tp(key: TranslationKey, params: Record<string, string>): string {
  return catalog[key].replace(/\{(\w+)\}/g, (match, name: string) => params[name] ?? match)
}

/** Lookup for keys assembled at runtime (e.g. `status.${state}`); falls back to the key itself. */
export function tDynamic(key: string): string {
  return (catalog as Record<string, string>)[key] ?? key
}
