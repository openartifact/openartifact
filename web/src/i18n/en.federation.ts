// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Federated repositories: health view, peers and inbound tokens. Prefix: `federation.*`.
export const enFederation = {
  'federation.tab': 'Federation',
  'federation.badge': 'federated',
  'federation.badgeTitle': 'This repository is federated with other instances.',
  'federation.title': 'Federation',
  'federation.description':
    'Keep this repository in sync with the same repository on other instances. Each instance pulls the changes of its peers; there is no leader.',
  'federation.loadFailed': 'Could not load the federation status.',
  'federation.enable': 'Federate this repository',
  'federation.enableHint':
    'Turning it on records every change in a log that peers pull, and adds the content already stored to that log.',
  'federation.disableHint':
    'Turning it off deletes this repository\'s change log and its peers here. The other instances keep their copy of the content.',
  'federation.enabled': 'Federation enabled',
  'federation.disabled': 'Federation disabled',
  'federation.toggleFailed': 'Could not change federation.',
  'federation.unsupported.title': 'Federation is not available for this repository',
  'federation.unsupported.hint':
    'Only hosted repositories of the raw, maven, npm, docker, helm, pypi, cargo, go and nuget formats can be federated. Proxy and group repositories cannot.',
  'federation.disableTitle': 'Turn off federation?',
  'federation.disableBody':
    'The change log and all peers of this repository are removed. Content already synchronized stays. Turning federation on again starts a new log, and peers must be added again.',
  'federation.disableConfirm': 'Turn off',

  // Log statistics
  'federation.stats.instance': 'Instance ID',
  'federation.stats.log': 'Log ID',
  'federation.stats.head': 'Log head',
  'federation.stats.units': 'Logged units',
  'federation.stats.tombstones': 'Deletions',

  // Peers
  'federation.peers.title': 'Peers',
  'federation.peers.hint': 'Instances this repository pulls changes from. Status refreshes every 10 seconds.',
  'federation.peers.add': 'Add peer',
  'federation.peers.empty': 'No peers yet',
  'federation.peers.emptyHint': 'Add a peer to start pulling changes from another instance.',
  'federation.peers.table': 'Federation peers',
  'federation.peers.column.name': 'Peer',
  'federation.peers.column.state': 'State',
  'federation.peers.column.lag': 'Lag',
  'federation.peers.column.lastSuccess': 'Last success',
  'federation.peers.column.lastError': 'Last error',
  'federation.peers.column.counts': 'Applied / conflicts',
  'federation.peers.column.deferred': 'Deferred',
  'federation.peers.column.enabled': 'Enabled',
  'federation.peers.never': 'Never',
  'federation.peers.noError': 'None',
  'federation.peers.showMore': 'Show more',
  'federation.peers.showLess': 'Show less',
  'federation.peers.backlog': '{n} to apply',
  'federation.peers.errorCount': '{n} consecutive failures',
  'federation.peers.nextAttempt': 'Next attempt {when}',
  'federation.peers.appliedConflicts': '{applied} / {conflicts}',
  'federation.peers.deferredView': 'View {n} deferred',
  'federation.peers.deferredNone': '0',
  'federation.peers.edit': 'Edit peer {name}',
  'federation.peers.delete': 'Delete peer {name}',
  'federation.peers.toggle': 'Poll peer {name}',
  'federation.peers.resync': 'Resync',
  'federation.peers.resyncAria': 'Resync peer {name}',
  'federation.peers.resyncPending': 'Resync pending',
  'federation.peers.resyncQueued': 'Resync queued for {name}.',
  'federation.peers.resyncFailed': 'Could not start the resync.',
  'federation.peers.resyncTitle': 'Resync {name}?',
  'federation.peers.resyncBody':
    'The cursor is reset and the whole log of the peer is read again and merged. Content that is already present is left unchanged. This can take a while and load the peer.',
  'federation.peers.resyncConfirm': 'Resync',
  'federation.peers.deleteTitle': 'Delete peer {name}?',
  'federation.peers.deleteBody':
    'This repository stops pulling from the peer and forgets its position. Content already synchronized stays.',
  'federation.peers.deleted': 'Peer {name} deleted.',
  'federation.peers.deleteFailed': 'Could not delete the peer.',
  'federation.peers.updateFailed': 'Could not update the peer.',

  // Peer states
  'federation.state.ok': 'In sync',
  'federation.state.lagging': 'Lagging',
  'federation.state.error': 'Error',
  'federation.state.pending': 'Pending',
  'federation.state.disabled': 'Disabled',

  // Lag
  'federation.lag.none': 'Caught up',

  // Peer dialog
  'federation.peerDialog.addTitle': 'Add peer',
  'federation.peerDialog.editTitle': 'Edit peer',
  'federation.peerDialog.name': 'Name',
  'federation.peerDialog.nameHint': 'A label for this peer, for example the site it runs in.',
  'federation.peerDialog.url': 'Base URL',
  'federation.peerDialog.urlHint': 'Base URL of the peer instance, for example https://artifacts.example.org.',
  'federation.peerDialog.token': 'Token',
  'federation.peerDialog.tokenHint': 'A federation token created on the peer for the same repository.',
  'federation.peerDialog.tokenKeep': 'Leave empty to keep the stored token.',
  'federation.peerDialog.tls': 'TLS (optional)',
  'federation.peerDialog.tlsKeep': 'Stored values are kept unless you paste a new one.',
  'federation.peerDialog.ca': 'CA certificate (PEM)',
  'federation.peerDialog.caStored': 'A CA certificate is stored.',
  'federation.peerDialog.clientCert': 'Client certificate (PEM)',
  'federation.peerDialog.clientCertStored': 'A client certificate is stored.',
  'federation.peerDialog.clientKey': 'Client key (PEM)',
  'federation.peerDialog.enabled': 'Poll this peer',
  'federation.peerDialog.nameRequired': 'Enter a name.',
  'federation.peerDialog.urlInvalid': 'Enter an http:// or https:// URL.',
  'federation.peerDialog.tokenRequired': 'Enter the token.',
  'federation.peerDialog.clientPair': 'A client certificate and its key must be given together.',
  'federation.peerDialog.add': 'Add peer',
  'federation.peerDialog.created': 'Peer {name} added.',
  'federation.peerDialog.updated': 'Peer {name} updated.',
  'federation.peerDialog.createFailed': 'Could not add the peer.',

  // Deferred units
  'federation.deferred.title': 'Deferred units of {name}',
  'federation.deferred.hint':
    'Units that could not be applied yet, for example because a dependency has not arrived. They are retried automatically.',
  'federation.deferred.empty': 'Nothing is deferred.',
  'federation.deferred.loadFailed': 'Could not load the deferred units.',
  'federation.deferred.column.unit': 'Unit',
  'federation.deferred.column.seq': 'Sequence',
  'federation.deferred.column.attempts': 'Attempts',
  'federation.deferred.column.reason': 'Reason',
  'federation.deferred.column.next': 'Next attempt',

  // Inbound tokens
  'federation.tokens.title': 'Inbound tokens',
  'federation.tokens.hint': 'Tokens that peers use to pull this repository. Create one per peer and revoke it when the peer is retired.',
  'federation.tokens.name': 'Token name',
  'federation.tokens.namePlaceholder': 'For example: site-b',
  'federation.tokens.create': 'Create token',
  'federation.tokens.created': 'Token {name} created.',
  'federation.tokens.createFailed': 'Could not create the token.',
  'federation.tokens.secretTitle': 'Federation token',
  'federation.tokens.empty': 'No inbound tokens.',
  'federation.tokens.table': 'Inbound federation tokens',
  'federation.tokens.column.name': 'Name',
  'federation.tokens.column.created': 'Created',
  'federation.tokens.column.lastUsed': 'Last used',
  'federation.tokens.column.status': 'Status',
  'federation.tokens.active': 'Active',
  'federation.tokens.revoked': 'Revoked',
  'federation.tokens.neverUsed': 'Never',
  'federation.tokens.revoke': 'Revoke',
  'federation.tokens.revokeAria': 'Revoke token {name}',
  'federation.tokens.revokeTitle': 'Revoke token {name}?',
  'federation.tokens.revokeBody': 'A peer using this token can no longer pull this repository. This cannot be undone.',
  'federation.tokens.revokeConfirm': 'Revoke',
  'federation.tokens.revoked.toast': 'Token {name} revoked.',
  'federation.tokens.revokeFailed': 'Could not revoke the token.',

  // Create-repository forms
  'federation.create.label': 'Federate this repository',
  'federation.create.hint': 'Keep a change log so other instances can sync this repository. Can be changed later in the Federation tab.',
} as const
