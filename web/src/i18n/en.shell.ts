// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shell and command palette strings of the web rationalization. Keys are prefixed `shell.`.
export const enShell = {
  // Landmarks and skip link
  'shell.skipToContent': 'Skip to main content',

  // Top-level areas
  'shell.nav.home': 'Home',
  'shell.nav.repos': 'Repositories',
  'shell.nav.packages': 'Packages',
  'shell.nav.projects': 'Projects',
  'shell.nav.bundles': 'Bundles',
  'shell.nav.security': 'Security',
  'shell.nav.settings': 'Settings',
  'shell.requiresAdmin': 'Requires administrator',
  'shell.settingsManagedByAdmins': 'Instance settings are managed by administrators',

  // Shown after the router sends a user away from a page they may not open
  'shell.denied.admin': 'That page is only available to administrators',
  'shell.denied.repoCreate': 'Creating a repository needs the maintainer role in a project',
  'shell.denied.hint': 'Ask an administrator for access.',

  // Header and user menu
  'shell.searchPill': 'Search or jump to…',
  'shell.userMenu': 'Account menu for {name}',
  'shell.role.admin': 'Administrator',
  'shell.role.member': 'Member',
  'shell.menu.profile': 'Profile',
  'shell.menu.tokens': 'Access tokens',
  'shell.menu.about': 'About',
  'shell.menu.signOut': 'Sign out',
  'shell.menu.theme': 'Theme',

  // Command palette
  'shell.palette.dialogLabel': 'Command palette',
  'shell.palette.inputLabel': 'Search or run a command',
  'shell.palette.placeholder': 'Search repositories, packages, settings, or type > for actions',
  'shell.palette.resultsLabel': 'Results',
  'shell.palette.esc': 'Esc',
  'shell.palette.requiresAdmin': 'Requires admin',
  'shell.palette.lockedAnnounce': '{title} requires an administrator',
  'shell.palette.actionHint': 'Action',
  'shell.palette.searching': 'Searching…',
  'shell.palette.noResults': 'No results',
  'shell.palette.noResultsHint': 'Try a package name, a repository, or a setting such as “retention”.',
  'shell.palette.noActions': 'No matching actions',
  'shell.palette.count.one': '1 result',
  'shell.palette.count.other': '{n} results',
  'shell.palette.tooBroad': 'Too many package matches. Refine the query, for example with name:, format: or repo:.',
  'shell.palette.partialError': 'Some results could not be loaded.',
  'shell.palette.retry': 'Retry',
  'shell.palette.footNavigate': 'navigate',
  'shell.palette.footOpen': 'open',
  'shell.palette.footActions': 'actions only',
  'shell.palette.group.recent': 'Recent',
  'shell.palette.group.pages': 'Pages',
  'shell.palette.group.repositories': 'Repositories',
  'shell.palette.group.packages': 'Packages',
  'shell.palette.group.projects': 'Projects',
  'shell.palette.group.bundles': 'Bundles',
  'shell.palette.group.settings': 'Settings',
  'shell.palette.group.users': 'Users',
  'shell.palette.group.actions': 'Actions',
  'shell.palette.group.search': 'Search',
  'shell.palette.checksum': 'Look up checksum in Packages',
  'shell.palette.searchPackages': 'Search all packages for “{q}”',
  'shell.palette.matches': 'Matches “{kw}” · {group}',

  // Palette actions
  'shell.action.createRepo': 'Create repository',
  'shell.action.createProject': 'Create project',
  'shell.action.createToken': 'Create access token',
  'shell.action.createUser': 'Create user',
  'shell.action.createBundle': 'Create release bundle',
  'shell.action.toggleTheme': 'Toggle theme',
  'shell.action.signOut': 'Sign out',
} as const
