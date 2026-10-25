// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

export const enAuth = {
  // Login page
  'login.tagline': 'Artifact repository · self-hosted',
  'login.heroTitleLine1': 'One registry for every',
  'login.heroTitleLine2': 'build artifact your team ships.',
  'login.heroBody':
    'Host, version, and distribute Docker images, npm packages, Maven artifacts, Python wheels, Helm charts, and more — all from a single, self-hosted registry your team already trusts.',
  'login.subheading': 'Welcome back. Sign in to manage your repositories.',
  'login.showPassword': 'Show password',
  'login.hidePassword': 'Hide password',
  'login.signingIn': 'Signing in…',
  'login.orContinueWith': 'or continue with',
  'login.continueWith': 'Continue with {name}',
  'login.ssoLoadFailed': 'Could not load single sign-on providers.',

  // Change-password flow
  'auth.passwordNotApplied': 'Password changed, but the session could not be refreshed. Please sign in again.',

  // Shell
  'shell.unexpectedError': 'Unexpected error',
  'shell.openPalette': 'Open command palette',
  'shell.paletteHotkey.apple': '⌘K',
  'shell.paletteHotkey.other': 'Ctrl K',
  'shell.searchPlaceholder': 'Search...',
  'shell.dismissToast': 'Dismiss notification',
  'shell.notifications': 'Notifications',
  'shell.primaryNav': 'Primary',
  'shell.breadcrumbs': 'Breadcrumbs',
  'shell.settingsSections': 'Settings sections',
  'shell.dataTable': 'Data table',
  'shell.notFoundTitle': 'Page not found',
  'shell.notFoundBody': 'The page you are looking for does not exist or has moved.',
  'shell.backToDashboard': 'Back to dashboard',
  'shell.projects': 'Projects',
  'shell.aiSettings': 'AI Settings',
  'shell.aiAnalysis': 'AI Analysis',

  // Settings: profile
  'settings.profile.admin': 'Admin',
  'settings.profile.email': 'Email',
  'settings.profile.fullName': 'Full name',
  'settings.profile.role': 'Role',
  'settings.profile.roleAdmin': 'Administrator',
  'settings.profile.roleUser': 'User',

  // Settings: tokens
  'settings.tokens.yourTokens': 'Your tokens',
  'settings.tokens.accessToken': 'Access token',
  'settings.tokens.kind': 'Kind',
  'settings.tokens.never': 'Never',
  'settings.tokens.column.label': 'Label',
  'settings.tokens.column.created': 'Created',
  'settings.tokens.column.lastUsed': 'Last used',
  'settings.tokens.column.expires': 'Expires',
  'settings.tokens.labelPlaceholder': 'e.g., CLI Token',
  'settings.tokens.revoke': 'Revoke',
  'settings.tokens.revokeConfirm': 'Revoke this token? Anything using it will stop working.',
  'settings.tokens.revoked': 'Token revoked',
  'settings.tokens.copied': 'Copied to clipboard',
  'settings.tokens.copyFailed': 'Failed to copy',
  'settings.tokens.createFailed': 'Failed to create token',
  'settings.tokens.revokeFailed': 'Failed to revoke token',
  'settings.tokens.loadFailed': 'Failed to load tokens',
} as const
