// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Repository list, create drawer, repository detail and shared trash list strings.
// Keys are prefixed `rl.`.
export const enRepoList = {
  'rl.trash.deletedBy': 'by {name}',
  'rl.trash.expires': 'Purges',
  'rl.trash.expiresTitle': 'Purged permanently on {date}',
  'rl.trash.item': 'Item',
  'rl.trash.caption': 'Deleted items',
  'rl.trash.emptyAction': 'Empty trash',
  'rl.trash.emptyBody': 'Permanently delete every item in the trash of "{name}"? The repository itself is kept. The items are destroyed and this cannot be undone.',
} as const
