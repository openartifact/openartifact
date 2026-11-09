<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Federation: replicating a repository between instances

Federation keeps a hosted repository identical on two or more OpenArtifact instances. Use it for sites that must keep working when the link between them is down. Any member accepts writes, and changes flow in both directions.

Each instance holds one *member* repository of the federated set. There is no coordinator, and replication survives network partitions and the loss of a whole member.

Federation **is**:

- Peer-to-peer replication of a hosted repository between two or more instances.
- Pull-based: each member reads the change log of its peers and fetches the content it is missing. A member behind a firewall can pull without being reachable.
- Topology-free: pairs, chains, stars and full meshes all converge.
- Asynchronous and eventually consistent: a change shows up on other members after up to one poll interval plus the transfer time.

Federation **is not**:

- Edge-node or release-bundle distribution. Every member is an equal, writable peer, and nothing is signed, packaged or pushed to nodes. This is deliberate, see the legal note in the [design](../design/federation.md#legal-constraint).
- Replication of proxy caches, group repositories, users, permissions, repository settings, scan results or quotas. Each member keeps its own.
- Synchronous or read-your-writes replication.

The full rationale is in the [design document](../design/federation.md).

## Set up two instances

Instance A is `https://a.example.com` with repository `libs`. Instance B is `https://b.example.com` with repository `libs-b`. Both repositories are hosted and have the same format. Names may differ per instance.

`TOKEN_A` and `TOKEN_B` are administrator API tokens of each instance. The global `manage` permission is required (see [security](#security)).

1. Set `federation.enabled: true` (or `OPENARTIFACT_FEDERATION_ENABLED=true`) on **every** instance and restart. This starts the pull workers and mounts the `/federation/v1` peer endpoints. See [configuration](../getting-started/configuration.md#federation-federation).
2. Set `OPENARTIFACT_SECRET_KEY` on every instance, because outbound tokens are stored encrypted. The instances must reach each other over `https`.
3. Enable federation for the repository on both sides:

   ```bash
   curl -sS -X PUT "https://a.example.com/api/v1/repositories/libs/federation" \
     -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
     -d '{"enabled": true}'
   curl -sS -X PUT "https://b.example.com/api/v1/repositories/libs-b/federation" \
     -H "Authorization: Bearer $TOKEN_B" -H 'Content-Type: application/json' \
     -d '{"enabled": true}'
   ```

4. Create an inbound peer token on A for B. The response contains the plaintext `oafed_...` token once, so store it now:

   ```bash
   curl -sS -X POST "https://a.example.com/api/v1/repositories/libs/federation/tokens" \
     -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
     -d '{"name": "b-site"}'
   ```

5. Add A as a peer on B, with A's base URL and the token from step 4:

   ```bash
   curl -sS -X POST "https://b.example.com/api/v1/repositories/libs-b/federation/peers" \
     -H "Authorization: Bearer $TOKEN_B" -H 'Content-Type: application/json' \
     -d '{"name": "site-a", "base_url": "https://a.example.com", "token": "oafed_..."}'
   ```

6. For two-way replication, repeat in reverse: create a token on B (`POST .../libs-b/federation/tokens`) and add B as a peer of A (`POST .../libs/federation/peers`, `base_url` `https://b.example.com`).

With only steps 1 to 5, B mirrors A one way. B pulls, and A never pulls from B. B's own uploads go into B's log, but nobody reads it.

Check progress and health on either side:

```bash
curl -sS "https://b.example.com/api/v1/repositories/libs-b/federation" \
  -H "Authorization: Bearer $TOKEN_B"
```

The status lists each peer with its cursor, backlog, lag, last success, last error, consecutive failures and deferred units. Upload an artifact to A. It appears on B within about a poll interval, and with two-way replication the reverse holds.

The same data is in the UI:

- **Repository Settings tab** (`/repos/NAME?tab=settings`), section **Federation**: enable or disable federation, create and revoke tokens, add, edit, pause and remove peers, press **Resync** and inspect deferred units. The token plaintext is displayed once.
- **Settings > Federation** (`/settings/federation`): the server state and every federated repository with how its peers are doing.

You can also create a repository federated from the start: `POST /api/v1/repositories` with `"federated": true`. This works for hosted repositories of a supported format, and only while `federation.enabled` is on. Enabling federation on a repository that already has content records every existing item in the log, so the first pull transfers the whole repository.

## Supported formats

| Format | Replicated unit |
|---|---|
| Cargo | crate version (yank state included) |
| Docker / OCI | manifest (with config and layers), tag |
| Go | module version |
| Helm | chart version |
| Maven | each file |
| npm | version, dist-tag |
| NuGet | package version |
| PyPI | file |
| raw | file path |

Any format not in this table is unsupported, and so are proxy and group repositories (not authoritative content). Only hosted repositories can be federated.

- **Conan, Conda, RubyGems**: their indexes or revision graphs are regenerated whole and need their own unit design.
- **Debian and RPM**: their metadata is signed with the instance's own key, and consistent signed indexes across members need their own design.
- **Ivy and sbt**: not a technical limit. They are left out of the first test matrix.
- **Terraform/OpenTofu**: not designed yet. Modules and provider platforms are not replicated.

Derived metadata (Helm `index.yaml`, Maven `maven-metadata.xml`, npm packuments, NuGet and PyPI indexes) is never copied. Each member regenerates it from the replicated units, so members hold the same content but not necessarily byte-identical metadata files. The formats of the member repositories must match.

## Conflicts and deletes

A *unit* is one replicated item: a package version, a file path, a tag. A unit row records which blob the unit points to, plus its metadata. Blobs are content-addressed, so they never conflict. A *conflict* is two members changing the same unit before either has seen the other's change.

A *tombstone* is the record of a delete in the change log. It tells the other members to delete the unit too.

### How conflicts are decided

- **The latest write wins, per unit.** Every write carries a timestamp from the database clock of the instance that made it, with the instance id as a tie-break. Every member compares the stamps and picks the same winner without talking to the others.
- **A write made after seeing another always wins over it.** Only truly concurrent writes are decided by time, then by instance id.
- **Tags move deterministically.** Two members moving the same npm dist-tag or Docker tag at once converge on one target on all members. The losing write is discarded.
- **Conflicts are counted.** A conflict is two members changing a unit with different results, neither having seen the other's change. Every change records the stamp of the state it replaced (its *base*). When a change from a peer replaces a local change, or is skipped because a newer local change exists, it is a conflict only if the newer write's base is older than the other state. A sequential overwrite, where a member changes a unit after having received the other member's change, is not a conflict, so moving a Docker tag or npm dist-tag from another site is not counted. The peer's conflict counter and `openartifact_federation_conflicts_total` increase, and a warning is logged. Members on an older version send no base stamps; a change from such a member that replaces a local change is counted as a conflict.
- **Immutable releases follow the same rule.** A concurrent publish of the same version with different content on two members is a conflict, and it converges by the latest-write rule even with `immutable_releases` on. That setting still blocks local overwrite attempts.

### Clock skew

A skewed clock can let a physically earlier write win, so keep instance clocks in sync with NTP.

A change stamped more than `federation.maxClockSkew` (default 5 minutes) ahead of the receiver's clock is not applied. The same holds for a change with an implausible counter. It is parked as `clock_skew` and applied by a later retry once the receiver's clock has caught up. Without this bound, one far-future stamp would make every later change of any member lose to it.

### Deletes

- **A delete reaches the other members as a tombstone.** Each member applies it as a normal delete: to the [trash](trash.md) with the repository's retention, or permanently when retention is `0`. `deleted_by` is `federation:<peer name>`.
- **An older concurrent upload does not bring the unit back.** The tombstone wins over a write that happened before the delete.
- **A restore from the trash is a new upload** for the other members.
- **A trash purge is not replicated.**
- **Removing a member repository does not delete content on other members.** They report the peer as failing (`404`).

### Tombstone retention

Tombstones are removed after `federation.tombstoneRetention` (default 30 days). A member that is offline for longer cannot learn the deletes that were collected. It still holds the artifact, and the next sync recreates it on the other members. Set the retention above the longest partition you want to survive.

## Fault tolerance

- **Partitions.** The peer position is a cursor kept by the receiver. When the link or either instance comes back, the pull continues from the cursor and catches up, however long the outage was (within tombstone retention).
- **Backoff.** A failing peer (network, `5xx`, rejected token) is retried with exponential backoff from 5 s up to `federation.maxBackoff` with jitter, and never given up. The first success resets it.
- **Poison units.** A unit that cannot be applied (quarantined at the origin, quota exceeded, blob missing at the origin, validation error) does not block the feed. A unit that is known to be unappliable for now (quarantined, quota, missing blob) is parked as *deferred* at once with the reason; one that fails for an unknown reason is parked as `apply_failed` after 3 failed pulls. The cursor moves on and the unit is retried with its own backoff (cap 1 h). List them with `GET .../peers/{id}/deferred` or in the UI.
- **No dropped events.** The cursor advances only after a unit is committed; a crash replays the entry and the stamp comparison makes it a no-op.
- **Large blobs.** Blob transfers use an idle timeout, so big files on slow links complete. A transfer that is cut midway is [resumed](#resuming-interrupted-transfers), not restarted.
- **Several replicas of one instance.** One replica pulls from a given peer at a time.

## Resuming interrupted transfers

Blobs are downloaded into a spool directory as a *partial file* named after the blob's sha256. When the connection drops, the partial stays. The retry asks the peer for the rest with `Range: bytes=<partial size>-` and appends it; a 10 GiB image layer cut at 9 GiB costs 1 GiB more, not 10.

- **Retries.** A dropped body is retried immediately, with backoff from 500 ms to 8 s. After five attempts in a row that move no bytes the unit stops for this pull and the peer backs off as usual (see [fault tolerance](#fault-tolerance)); the partial is kept and the next pull resumes it. Attempts that make progress do not count against the limit.
- **Verification.** The sha256 is computed over the whole blob, the kept prefix included (it is re-read and hashed on every resume, so a damaged partial cannot slip through). Only a blob with the digest the unit declares is stored. A mismatch deletes the partial and defers the unit as `digest_mismatch`.
- **Declared size.** A unit must declare the size of each blob: positive, or `0` only for the empty blob. The download is held to it (the body is never read past it; a shorter or longer body is `digest_mismatch`), the quota is checked against it before anything is fetched, and the size recorded for the blob is the number of bytes that were received and hashed. A blob larger than `server.maxBodyBytes` is parked as `blob_too_large`.
- **Digests prove nothing about ownership.** A blob already stored for another repository of the instance (a private one, say) is not linked to the federated repository because a peer names its digest: it is downloaded from the peer and verified, so the peer has to have the bytes. Only a blob the repository itself already references is reused without a download.
- **Peers that cannot resume.** A peer that answers `200` to a range request (an older version, or a proxy that strips `Range`) restarts the transfer from 0. A peer that answers `416` for a partial the blob no longer fits (it was replaced) also restarts from 0.
- **Limits.** `federation.maxPartialBytes` (default 10 GiB) caps the spool; when it is exceeded the partial written longest ago is removed first, except ones being downloaded. `federation.maxPartialAge` (default 24 h) removes partials not written for that long. Both run at startup and every 10 minutes.
- **Where.** `federation.spoolDir` (`OPENARTIFACT_FEDERATION_SPOOL_DIR`). Empty uses a directory under the OS temp dir, which is gone after a container restart; point it at a persistent volume to resume across restarts. Give each instance its own directory: instances sharing one would sweep each other's files. It also needs room for the largest blob in flight plus the partials you allow.
- **Nothing is pushed.** Resuming is still the receiver asking for bytes of a blob that a unit it already fetched lists; the peer authorises each range request with the same unit-membership check as a full download.

Watch `openartifact_federation_resumed_total` (resumed transfers per repository and peer) and `openartifact_federation_partial_bytes` (size of the spool). A steadily growing partial size with no resumes means partials accumulate for units that never complete; check the deferred list.

## Resync, rebuild and restore

- **Resync**: the **Resync** button or `POST .../peers/{id}/resync` resets the cursor to 0 and merges the peer's whole state: units already up to date cost one comparison and missing blobs are refetched. It never deletes anything because the peer lacks it. Units that still cannot be applied go back to the deferred list. It is safe at any time. A peer whose log was reset is detected and resynced automatically.
- **Rebuilding a lost member**: create the repository on the replacement instance, enable federation, add a surviving member as a peer with a token issued there, and the new member pulls everything. Survivors that should pull from the new member need a token issued by it: edit the peer entry (`PATCH .../peers/{id}`, `token`) or add a new peer. A rebuilt member has a new log id, so survivors detect it and resync automatically.
- **Restore from a backup**: a restored member may be behind the position its peers already passed. Press **Resync** on every peer of the restored member (until then the status shows an error because the peer head is below the cursor). See also [backup](backup.md).

## Sharing VEX decisions

Members can share their [VEX decisions](../security/sbom.md#vex), so an assessment made on one site reaches the others. Sharing is off by default on both sides. The publishing member opts in per repository, and each receiving member chooses per peer how much it trusts the decisions.

1. On the publishing member, switch on `vex_publish` for the federated repository:

   ```bash
   curl -sS -X PUT "https://a.example.com/api/v1/repositories/libs/federation" \
     -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
     -d '{"enabled": true, "vex_publish": true}'
   ```

2. On the receiving member, set `vex_trust` on the peer:

   ```bash
   curl -sS -X PATCH "https://b.example.com/api/v1/repositories/libs-b/federation/peers/<PEER_ID>" \
     -H "Authorization: Bearer $TOKEN_B" -H 'Content-Type: application/json' \
     -d '{"vex_trust": "suggest"}'
   ```

`vex_trust` can also be given when you add the peer. In the UI, both settings are in the **Federation** section of the repository's Settings tab.

| `vex_trust` | Effect |
|---|---|
| `ignore` | default; nothing is fetched |
| `suggest` | the peer's decisions become suggestions for an admin to accept or dismiss |
| `apply` | the peer's decisions are recorded at once where the artifact has no decision of another source; the others stay suggestions |

### What is shared

- Only decisions recorded on the publishing member itself are published. Statements it imported from a [vendor feed](../security/vex-feeds.md) or took over from another peer are not, so decisions never echo back.
- A decision applies to the receiving member's artifact with the same package URL, and only when that artifact has a vulnerability scan.
- A repository without `vex_publish` answers the VEX request with `404`. The receiver records that in `vex_error` and carries on.

### When decisions are fetched

The receiver fetches the peer's decisions after a successful pull, at most once an hour. The time of the last fetch is the peer's `vex_synced_at`. A failed fetch sets `vex_error` and never fails the pull. Like `last_error`, it is a short reason and the detail is in the server log.

### Review suggestions

A peer never silently overrides a decision of this member. A decision that would replace a local one, a vendor statement or another peer's decision stays a suggestion until an admin acts on it.

```bash
curl -sS "https://b.example.com/api/v1/repositories/libs-b/vex/suggestions?state=pending" \
  -H "Authorization: Bearer $TOKEN_B"
curl -sS -X POST "https://b.example.com/api/v1/repositories/libs-b/vex/suggestions/<ID>/accept" \
  -H "Authorization: Bearer $TOKEN_B"
curl -sS -X POST "https://b.example.com/api/v1/repositories/libs-b/vex/suggestions/<ID>/dismiss" \
  -H "Authorization: Bearer $TOKEN_B"
```

- `state` is `pending` (default), `accepted`, `dismissed` or `all`.
- A suggestion shows the peer's status, justification and statements, the peer it came from, and the local decision it would replace (`local_status`, `local_source_kind`).
- **Accept** records the decision with source `peer` and replaces whatever decision the artifact had. It answers with the recorded statement.
- **Dismiss** keeps the current decision. The suggestion stays dismissed until the peer changes its decision.
- A suggestion whose decision changes on the peer goes back to `pending`. A suggestion the peer no longer publishes is removed.
- Switching a peer to `ignore` removes its suggestions.
- Accepting and dismissing need an administrator of the repository. They are recorded in the audit log as `vex.suggestion_accepted` and `vex.suggestion_dismissed`.

The suggestions are also listed in the UI, with accept and dismiss actions.

## Monitoring

Metrics, all with the labels `repo` and `peer` (except `_applied_total`, which also has `result`, and `openartifact_federation_partial_bytes`, the size in bytes of the spool directory's partial files, which has no labels):

| Metric | Meaning |
|---|---|
| `openartifact_federation_lag_seconds` | Age of the oldest change not yet applied from the peer |
| `openartifact_federation_backlog_units` | Units known on the peer and not yet applied |
| `openartifact_federation_applied_total{result}` | Units applied, by result |
| `openartifact_federation_conflicts_total` | Concurrent writes decided by last-writer-wins |
| `openartifact_federation_pull_errors_total` | Failed pull attempts |
| `openartifact_federation_last_success_timestamp_seconds` | Unix time of the last successful pull |
| `openartifact_federation_resumed_total` | Blob transfers continued with a `Range` request after an interruption |

Example alert rules:

```yaml
groups:
  - name: openartifact-federation
    rules:
      - alert: FederationLagHigh
        expr: openartifact_federation_lag_seconds > 900
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.repo }} is {{ $value | humanizeDuration }} behind {{ $labels.peer }}"
      - alert: FederationPeerDown
        expr: time() - openartifact_federation_last_success_timestamp_seconds > 3600
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "No successful pull of {{ $labels.repo }} from {{ $labels.peer }} for an hour"
      - alert: FederationPullErrors
        expr: increase(openartifact_federation_pull_errors_total[15m]) > 5
        labels: {severity: warning}
      - alert: FederationBacklogGrowing
        expr: openartifact_federation_backlog_units > 1000 and deriv(openartifact_federation_backlog_units[30m]) > 0
        for: 30m
        labels: {severity: warning}
```

Set the lag alert below `federation.tombstoneRetention`; a peer that stays behind longer risks resurrecting deleted artifacts. The health fields (lag, backlog, last success, last error, consecutive failures, deferred count, remote head versus cursor) are in `GET .../federation` and the Federation section of the repository's Settings tab. General metrics setup is in [monitoring](monitoring.md).

## Security

- **Peer tokens** (`oafed_...`) are a separate credential bound to **one repository**. Only their hash is stored; the plaintext is shown once at creation. They grant read access to the `/federation/v1` endpoints of that repository and nothing else: no REST API, no format endpoint, no other repository. User tokens, sessions and the legacy key are rejected there. Tokens can be listed (name, created, last used, last cursor) and revoked; issue and revoke show up in the request audit log.
- **Outbound tokens** (the token you give when adding a peer) are stored encrypted with `OPENARTIFACT_SECRET_KEY` and never returned by the API. The token is sent to the peer's URL, so changing `base_url` with `PATCH` requires the token in the same request (`400 validation_failed` otherwise); the stored token is replaced, never carried over to the new URL.
- **Errors are classified, not copied.** `last_error` of a peer and of a deferred unit is one of a fixed set of reasons, with the HTTP status when the peer answered one, for example `unreachable`, `timeout`, `tls`, `unauthorized (HTTP 401)`, `not_found (HTTP 404)`, `rate_limited (HTTP 429)`, `upstream_error (HTTP 502)`, `protocol_mismatch`, `blocked_address`, `invalid_config`, `invalid_response`, `invalid_unit`, `blob_rejected`, `clock_skew`, `quota_exceeded`, `internal_error`. Text of the peer, the network stack or the database is not stored or returned; the full cause is in the server log (`federation: pull failed`, `federation: unit set aside`).
- **Transport.** Peer URLs must be `https`. Plain `http` and private or loopback addresses are accepted only with `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` (closed networks, development). Outbound calls use the SSRF-safe client: addresses are checked at connection time and redirects to private addresses are refused.
- **CA pin and client certificate.** A peer entry can carry a CA bundle to trust for that peer (private PKI) and a client certificate and key presented to it; both are stored encrypted. Inbound mTLS is terminated by the reverse proxy in front of the peer; let it restrict who can reach `/federation/`. The token is still required, so mTLS is defence in depth.
- **RBAC.** Managing peers and tokens, resync and the health view need the global `manage` permission (administrators); the federation routes are not delegated to project owners. Creating a repository with `federated: true` is likewise limited to administrators. Federation never gives read or write access to artifacts on a member; client access stays governed by that member's own permissions.
- **Quarantine** is local. A unit quarantined at the origin is not served (`423`) and is deferred on the receiver until released; replicated content is scanned by the receiver under its own policies.

## Audit and webhooks

Changes applied from a peer emit the normal `artifact.uploaded` / `artifact.deleted` webhooks of the receiving member, like a local change; the origin is visible in `deleted_by` (`federation:<peer name>`) for deletes. Peer, token and enable/disable operations are admin API calls; they are recorded by the generic request audit entries (`http.request`, with the route and actor) like other admin changes.

## Instance identity

Every instance generates an id when its database is first migrated, and a peer that reports the id of the instance pulling from it is refused. A database copy used as a second member (a clone or a restore booted next to the original) therefore cannot peer with its source. Build a replacement member empty and let it pull instead.

## Limits

- No Conan, Conda, Debian, Ivy, RPM, RubyGems, sbt or Terraform; proxy and group repositories cannot be federated.
- Eventual consistency only; replication latency is about the poll interval.
- Resume needs the spool to survive: with the default (under the OS temp dir) partials do not outlive a container restart.
- Quotas apply on each side. A unit that exceeds the receiver's quota is deferred, not forced in. Trashed bytes keep counting as for local deletes.
- Applied deletes follow the receiver's [trash](trash.md) settings.
- A repository that is `blocked` refuses applies.
- One writer per federated repository at a time for the duration of a write transaction (sequence numbers are allocated in commit order). Uploads are short, but heavy parallel ingestion into a federated repository is slower than into a plain one.
- Disabling federation on a repository drops its log and peers; other members are unaffected.

## Admin API

All under `/api/v1/repositories/{name}/federation`, permission `manage`.

| Method and path | Purpose |
|---|---|
| `GET /` | Status: enabled flag, tokens, peers with health |
| `PUT /` | Enable or disable: `{"enabled": true}`; optional `vex_publish` |
| `POST /peers` | Add a peer (name, base URL, outbound token, optional CA pin and client certificate) |
| `PATCH /peers/{id}` | Edit or pause a peer |
| `DELETE /peers/{id}` | Remove a peer |
| `POST /peers/{id}/resync` | Reset the cursor and merge the peer's full state |
| `GET /peers/{id}/deferred` | Deferred units with reason and attempts |
| `POST /tokens` | Create an inbound peer token (plaintext returned once) |
| `DELETE /tokens/{id}` | Revoke an inbound peer token |

## Peer protocol

Used between instances, authenticated with `Authorization: Bearer oafed_...`. The repository is implied by the token.

| Endpoint | Purpose |
|---|---|
| `GET /federation/v1/info` | Handshake: protocol, instance id, log id, head sequence, repository name and format |
| `GET /federation/v1/log?after=&limit=` | Change feed: units changed after a sequence number |
| `GET /federation/v1/unit?key=` | Current state of one unit |
| `GET /federation/v1/blob?unit=&sha256=` | One blob of that unit; honours `Range: bytes=N-` (`206`, or `416` for an unsatisfiable or unsupported range) |
| `GET /federation/v1/vex` | The repository's own VEX decisions as an OpenVEX document, when [`vex_publish`](#sharing-vex-decisions) is on; `404` otherwise |

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| Peer endpoints return `404` | `federation.enabled` is off on that instance, or the repository is not federated there |
| `401` from the peer | Token revoked, mistyped or issued for another repository; create a new token and update the peer |
| Peer URL rejected | Not `https`, or a private address without `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` |
| TLS error | Private CA: pin it in the peer's CA bundle; check the client certificate if the proxy requires mTLS |
| Format mismatch error | The two repositories have different formats; federation needs equal formats |
| Lag grows, `consecutive_failures` rises | Peer unreachable or erroring; read `last_error` (a reason from the list under [security](#security); the detail is in the server log). It retries up to `maxBackoff` and catches up on return |
| Deferred units | Open the deferred list for the reason: quarantined at the origin (release it there), quota exceeded (raise the quota), blob missing at the origin, `apply_failed` (an unknown failure that repeated: read the server log, `federation: unit set aside`), `clock_skew` (the peer's clock is ahead: fix its NTP, the unit applies once the local clock catches up or `federation.maxClockSkew` is raised), `blob_too_large` or `invalid_blob` (the peer declares a size that is over `server.maxBodyBytes` or not valid); then Resync |
| Error "head below cursor" or the peer's log was reset | The peer was restored from a backup or rebuilt; press Resync |
| Deleted artifact came back | The deleting member's tombstone expired before the other member synced; delete again and raise `tombstoneRetention` |
| Two members disagree on a tag | Concurrent moves are still converging, or clocks are skewed; check `conflicts_total` and NTP |
| Index files differ byte for byte | Expected: metadata is regenerated per member |
| Uploads slow on a federated repository | Writers serialise on the log head; see [limits](#limits) |
