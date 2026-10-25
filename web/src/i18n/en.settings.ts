// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Settings strings of the web rationalization. Keys are prefixed `settings.`.
export const enSettings = {
  // Hub
  'settings.hub.subtitleAdmin': 'Everything you can configure on this instance, grouped by what you are trying to do. Search by what you want to change.',
  'settings.hub.subtitleUser': 'Your profile and credentials.',
  'settings.hub.searchLabel': 'Search settings',
  'settings.hub.searchPlaceholder': 'Try “sso”, “retention” or “s3 bucket”',
  'settings.hub.searchPlaceholderUser': 'Try “password” or “ci”',
  'settings.hub.examples': 'For example',
  'settings.hub.resultsTitle': 'Settings matching “{q}”',
  'settings.hub.resultsCount.one': '{count} setting matches “{q}”',
  'settings.hub.resultsCount.other': '{count} settings match “{q}”',
  'settings.hub.rankedHint': 'Title matches come first.',
  'settings.hub.matchesIn': 'Matches “{q}” in {fields}',
  'settings.hub.field.title': 'title',
  'settings.hub.field.keyword': 'keywords',
  'settings.hub.field.description': 'description',
  'settings.hub.field.id': 'section name',
  'settings.hub.field.words': 'title, description and keywords',
  'settings.hub.lockedMatches.one': '{count} more setting matches but is managed by administrators.',
  'settings.hub.lockedMatches.other': '{count} more settings match but are managed by administrators.',
  'settings.hub.empty.title': 'No settings match “{q}”',
  'settings.hub.empty.hint': 'Try a shorter or broader word, for example “storage”. The command palette also searches repositories, packages and findings.',
  'settings.hub.empty.searchStorage': 'Search for “storage”',
  'settings.hub.empty.openPalette': 'Open the command palette',
  'settings.hub.clearSearch': 'Clear search',
  'settings.hub.lockedGroup': 'Managed by administrators',

  // Section page
  'settings.page.allSettings': 'All settings',
  'settings.page.jumpTo': 'Go to section',
  'settings.page.forbidden.title': '{title} requires an administrator',
  'settings.page.forbidden.hint': 'You are signed in as {user}. Ask an administrator for access.',
  'settings.page.loadFailed.title': 'This page failed to load',
  'settings.page.loadFailed.hint': 'The page could not be downloaded. Check your connection and try again.',
  'settings.page.retry': 'Try again',

  // Users
  'settings.users.counts': '{total} users, {active} active, {disabled} disabled',
  'settings.users.emptyHint': 'Create a user to give someone access, or let people sign in through single sign-on.',
} as const
