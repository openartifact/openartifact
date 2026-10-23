<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Federation: replicating a repository between instances

A federated repository is a set of *member* repositories, one per OpenArtifact instance, that converge to the same content. Any member accepts writes and changes flow in both directions. Replication survives network partitions and the loss of a whole member, with no coordinator.

Federation **is**:

- Peer-to-peer replication of a hosted repository between two or more instances.
- Pull-based: each member pulls the change log of its peers by cursor and fetches the content it is missing. A member behind a firewall can pull without being reachable.
- Topology-free: pairs, chains, stars and full meshes all converge.
- Asynchronous and eventually consistent: a change shows up on other members after up to one poll interval plus the transfer time.

Federation **is not**:

- Edge-node or release-bundle distribution. Every member is an equal, writable peer; nothing is signed, packaged or pushed to nodes. This is deliberate, see the legal note in the [design](../design/federation.md#legal-constraint).
- Replication of proxy caches, group repositories, users, permissions, repository settings, scan results or quotas. Each member keeps its own.
- Synchronous or read-your-writes replication.

The full rationale is in the [design document](../design/federation.md).

## Supported formats

| Format | Replicated unit |
|---|---|
| raw | file path |
| Maven | each file |
| npm | version, dist-tag |
| Docker / OCI | manifest (with config and layers), tag |
| Helm | chart version |
| PyPI | file |
| Cargo | crate version (yank state included) |
| Go | module version |
| NuGet | package version |

Not supported, with the reason:

- **Debian and RPM**: their metadata is signed with the instance's own key; consistent signed indexes across members need their own design.
- **RubyGems, Conda, Conan**: their indexes or revision graphs are regenerated whole and need their own unit design.
- **Ivy and sbt**: not a technical limit; left out of the first test matrix.
- **Proxy and group repositories**: not authoritative content. Only hosted repositories can be federated.

Derived metadata (Maven `maven-metadata.xml`, npm packuments, Helm `index.yaml`, PyPI and NuGet indexes) is never copied. Each member regenerates it from the replicated units, so members hold the same content but not necessarily byte-identical metadata files.

Member repository names may differ per instance; the formats must match.

## Enable federation

1. Set `federation.enabled: true` (or `OPENARTIFACT_FEDERATION_ENABLED=true`) on **every** instance and restart. It starts the pull workers and mounts the `/federation/v1` peer endpoints. See [configuration](../getting-started/configuration.md#federation-federation).
2. Make sure `OPENARTIFACT_SECRET_KEY` is set (the outbound tokens are stored encrypted) and that the instances can reach each other over `https`.
3. On instance A, enable federation for the repository, then create an **inbound peer token** for it. The token is shown once.
4. On instance B, enable federation for its repository and add A as a **peer**: A's base URL and the token from step 3.
5. For two-way replication, repeat in reverse: create a token on B and add B as a peer on A.

With only steps 3 and 4, B mirrors A one way: B pulls, A never pulls from B (B's own uploads still go into B's log but nobody reads it).

A new repository can be created federated: `POST /api/v1/repositories` with `"federated": true` (hosted repositories of a supported format only, and only while `federation.enabled` is on). Enabling federation on a repository that already has content records every existing item in the log in one transaction, so the first pull transfers the whole repository.

## Walkthrough: two instances

Instance A is `https://a.example.com` with repository `libs`, instance B is `https://b.example.com` with repository `libs-b`; both are hosted and the same format. `TOKEN_A` and `TOKEN_B` are administrator API tokens of each instance (the `manage` permission is required, see [security](#security)).

Enable on both sides, using the same endpoint on each (`PUT .../federation`):

```bash
curl -sS -X PUT "https://a.example.com/api/v1/repositories/libs/federation" \
  -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
  -d '{"enabled": true}'
curl -sS -X PUT "https://b.example.com/api/v1/repositories/libs-b/federation" \
  -H "Authorization: Bearer $TOKEN_B" -H 'Content-Type: application/json' \
  -d '{"enabled": true}'
```

Create an inbound token on A for B. The response contains the plaintext `oafed_...` token once; store it now.

```bash
curl -sS -X POST "https://a.example.com/api/v1/repositories/libs/federation/tokens" \
  -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
  -d '{"name": "b-site"}'
```

Add A as a peer on B:

```bash
curl -sS -X POST "https://b.example.com/api/v1/repositories/libs-b/federation/peers" \
  -H "Authorization: Bearer $TOKEN_B" -H 'Content-Type: application/json' \
  -d '{"name": "site-a", "base_url": "https://a.example.com", "token": "oafed_..."}'
```

For two-way replication, create a token on B (`POST .../libs-b/federation/tokens`) and add B as a peer of A with `POST .../libs/federation/peers`, `base_url` `https://b.example.com`.

Check progress and health on either side:

```bash
curl -sS "https://b.example.com/api/v1/repositories/libs-b/federation" \
  -H "Authorization: Bearer $TOKEN_B"
```

The status lists the peers with their cursor, backlog, lag, last success, last error, consecutive failures and deferred units. The same data is on the repository's **Federation** tab in the web UI, where you can enable or disable federation, create and revoke tokens (the plaintext is displayed once), add, edit, pause and remove peers, press **Resync** and inspect deferred units.

Upload an artifact to A and it appears on B within about a poll interval; with two-way replication the reverse holds.

## Conflicts and deletes

Conflicts exist only on unit rows (which blob a path or tag points to, plus its metadata). Blobs are content-addressed and never conflict.

- **Last writer wins per unit.** Every write carries a hybrid logical clock stamp (wall time of the database clock, a counter, and the writer's instance id as tie-break). Every member computes the same winner without talking to the others. A write made after seeing another is always ordered after it; only truly concurrent writes are decided by time, then by instance id.
- **Tags move deterministically.** Two members moving the same npm dist-tag or Docker tag concurrently converge on one target on all members. The losing write is discarded.
- **Conflicts are counted, not silent.** When a replicated change replaces a local live unit with different content, or is skipped because a newer different one exists, the peer's conflict counter and `openartifact_federation_conflicts_total` increase and a warning is logged.
- **Clock skew** can let a physically earlier write win. Keep instance clocks in sync with NTP.
- **Immutable releases.** A concurrent publish of the same version with different content on two members is a conflict; federation converges it by the same rule, even with `immutable_releases` on (which still protects against local overwrite attempts).
- **Deletes propagate as tombstones.** A delete is an event: it is applied on the other members as a normal delete (to the [trash](trash.md) with the repository's retention, or permanent when retention is `0`; `deleted_by` is `federation:<peer name>`), and an older concurrent upload does not resurrect it. A restore from the trash on one member is a new upload for the others. A trash purge is not replicated.
- **Tombstone retention.** Tombstones are removed after `federation.tombstoneRetention` (default 30 days). A member offline for longer than that cannot learn the deletes that were collected; it still holds the artifact and the next sync recreates it on the other members. Set the retention above the longest partition you want to survive.
- **Removing a repository is not a delete of its content.** Deleting a member repository never deletes artifacts on other members; they report the peer as failing (`404`).

## Fault tolerance

- **Partitions.** The peer position is a cursor kept by the receiver. When the link or either instance comes back, the pull continues from the cursor and catches up, however long the outage was (within tombstone retention).
- **Backoff.** A failing peer (network, `5xx`, rejected token) is retried with exponential backoff from 5 s up to `federation.maxBackoff` with jitter, and never given up. The first success resets it.
- **Poison units.** A unit that cannot be applied (quarantined at the origin, quota exceeded, blob missing at the origin, validation error) does not block the feed. A unit that is known to be unappliable for now (quarantined, quota, missing blob) is parked as *deferred* at once with the reason; one that fails for an unknown reason is parked after 3 failed pulls. The cursor moves on and the unit is retried with its own backoff (cap 1 h). List them with `GET .../peers/{id}/deferred` or in the UI.
- **No dropped events.** The cursor advances only after a unit is committed; a crash replays the entry and the stamp comparison makes it a no-op.
- **Large blobs.** Blob transfers use an idle timeout, so big files on slow links complete.
- **Several replicas of one instance.** One replica pulls from a given peer at a time.

## Resync, rebuild and restore

- **Resync**: the **Resync** button or `POST .../peers/{id}/resync` resets the cursor to 0 and merges the peer's whole state: units already up to date cost one comparison and missing blobs are refetched. It never deletes anything because the peer lacks it. Units that still cannot be applied go back to the deferred list. It is safe at any time. A peer whose log was reset is detected and resynced automatically.
- **Rebuilding a lost member**: create the repository on the replacement instance, enable federation, add a surviving member as a peer with a token issued there, and the new member pulls everything. Survivors that should pull from the new member need a token issued by it: edit the peer entry (`PATCH .../peers/{id}`, `token`) or add a new peer. A rebuilt member has a new log id, so survivors detect it and resync automatically.
- **Restore from a backup**: a restored member may be behind the position its peers already passed. Press **Resync** on every peer of the restored member (until then the status shows an error because the peer head is below the cursor). See also [backup](backup.md).

## Monitoring

Metrics, all with the labels `repo` and `peer` (except `_applied_total`, which also has `result`):

| Metric | Meaning |
|---|---|
| `openartifact_federation_lag_seconds` | Age of the oldest change not yet applied from the peer |
| `openartifact_federation_backlog_units` | Units known on the peer and not yet applied |
| `openartifact_federation_applied_total{result}` | Units applied, by result |
| `openartifact_federation_conflicts_total` | Concurrent writes decided by last-writer-wins |
| `openartifact_federation_pull_errors_total` | Failed pull attempts |
| `openartifact_federation_last_success_timestamp_seconds` | Unix time of the last successful pull |

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

Set the lag alert below `federation.tombstoneRetention`; a peer that stays behind longer risks resurrecting deleted artifacts. The health fields (lag, backlog, last success, last error, consecutive failures, deferred count, remote head versus cursor) are in `GET .../federation` and the repository's Federation tab. General metrics setup is in [monitoring](monitoring.md).

## Security

- **Peer tokens** (`oafed_...`) are a separate credential bound to **one repository**. Only their hash is stored; the plaintext is shown once at creation. They grant read access to the four `/federation/v1` endpoints of that repository and nothing else: no REST API, no format endpoint, no other repository. User tokens, sessions and the legacy key are rejected there. Tokens can be listed (name, created, last used, last cursor) and revoked; issue and revoke show up in the request audit log.
- **Outbound tokens** (the token you give when adding a peer) are stored encrypted with `OPENARTIFACT_SECRET_KEY` and never returned by the API.
- **Transport.** Peer URLs must be `https`. Plain `http` and private or loopback addresses are accepted only with `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` (closed networks, development). Outbound calls use the SSRF-safe client: addresses are checked at connection time and redirects to private addresses are refused.
- **CA pin and client certificate.** A peer entry can carry a CA bundle to trust for that peer (private PKI) and a client certificate and key presented to it; both are stored encrypted. Inbound mTLS is terminated by the reverse proxy in front of the peer; let it restrict who can reach `/federation/`. The token is still required, so mTLS is defence in depth.
- **RBAC.** Managing peers and tokens, resync and the health view need the global `manage` permission (administrators); the federation routes are not delegated to project owners. Creating a repository with `federated: true` is likewise limited to administrators. Federation never gives read or write access to artifacts on a member; client access stays governed by that member's own permissions.
- **Quarantine** is local. A unit quarantined at the origin is not served (`423`) and is deferred on the receiver until released; replicated content is scanned by the receiver under its own policies.

## Audit and webhooks

Changes applied from a peer emit the normal `artifact.uploaded` / `artifact.deleted` webhooks of the receiving member, like a local change; the origin is visible in `deleted_by` (`federation:<peer name>`) for deletes. Peer, token and enable/disable operations are admin API calls; they are recorded by the generic request audit entries (`http.request`, with the route and actor) like other admin changes.

## Instance identity

Every instance generates an id when its database is first migrated, and a peer that reports the id of the instance pulling from it is refused. A database copy used as a second member (a clone or a restore booted next to the original) therefore cannot peer with its source. Build a replacement member empty and let it pull instead.

## Limits

- No Debian, RPM, RubyGems, Conda, Conan, Ivy or sbt; proxy and group repositories cannot be federated.
- Eventual consistency only; replication latency is about the poll interval.
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
| `PUT /` | Enable or disable: `{"enabled": true}` |
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
| `GET /federation/v1/blob?unit=&sha256=` | One blob of that unit |

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| Peer endpoints return `404` | `federation.enabled` is off on that instance, or the repository is not federated there |
| `401` from the peer | Token revoked, mistyped or issued for another repository; create a new token and update the peer |
| Peer URL rejected | Not `https`, or a private address without `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` |
| TLS error | Private CA: pin it in the peer's CA bundle; check the client certificate if the proxy requires mTLS |
| Format mismatch error | The two repositories have different formats; federation needs equal formats |
| Lag grows, `consecutive_failures` rises | Peer unreachable or erroring; read `last_error`. It retries up to `maxBackoff` and catches up on return |
| Deferred units | Open the deferred list for the reason: quarantined at the origin (release it there), quota exceeded (raise the quota), blob missing at the origin; then Resync |
| Error "head below cursor" or the peer's log was reset | The peer was restored from a backup or rebuilt; press Resync |
| Deleted artifact came back | The deleting member's tombstone expired before the other member synced; delete again and raise `tombstoneRetention` |
| Two members disagree on a tag | Concurrent moves are still converging, or clocks are skewed; check `conflicts_total` and NTP |
| Index files differ byte for byte | Expected: metadata is regenerated per member |
| Uploads slow on a federated repository | Writers serialise on the log head; see [limits](#limits) |
