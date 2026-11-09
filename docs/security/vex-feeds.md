<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Vendor VEX feeds

Use a VEX feed to import the exploitability statements a vendor publishes in CSAF 2.0. When a vendor says one of its packages is `not_affected` by a vulnerability or has `fixed` it, the server records that for every matching artifact your scans flagged, and the [finding](findings.md) is suppressed as if an admin had decided it.

Feeds are instance-wide and admin only.

## Add a feed

In the web UI, open **Settings > VEX feeds** and add a feed with a name and the vendor's URL. The list shows each feed's last sync, its last error and how many statements it holds and applies.

With the API, send an admin [API token](authentication.md#api-tokens):

```sh
curl -sS -X POST HOST/api/v1/vex/feeds -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"name":"acme","url":"https://acme.example.com/.well-known/csaf/provider-metadata.json"}'
```

The answer is `201` with the feed. The server syncs a new feed on its next feed check, then every `interval_hours`. To sync now:

```sh
curl -sS -X POST HOST/api/v1/vex/feeds/<ID>/sync -H 'Authorization: Bearer <TOKEN>'
```

The sync runs in the background and the call answers `202` with `{"status":"started"}`. A second request while that feed is still syncing answers `409 conflict`. The feed list in the UI has the same sync action.

## Feed settings

| Field | Default | Meaning |
|---|---|---|
| `name` | | unique name, shown as the source of the statements |
| `url` | | where the vendor publishes, see [accepted URLs](#accepted-urls) |
| `enabled` | `true` | a disabled feed is not synced; its statements stay |
| `require_signature` | `true` | reject documents without a valid signature |
| `pgp_keys` | empty | armored OpenPGP public keys to trust for this feed, at most 64 KiB |
| `interval_hours` | `24` | hours between syncs, 1 to 720 |

`PUT /api/v1/vex/feeds/{id}` replaces all of these fields. Changing the URL makes the next sync read every document again.

### Accepted URLs

- A CSAF `provider-metadata.json`. The server follows its distributions to the documents.
- A ROLIE feed (JSON). The server reads the documents it lists.
- A single CSAF document.

The URL must be `https`. Plain `http` is accepted only for a loopback address. Private addresses are refused unless `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`, as for other outbound URLs.

## How documents are checked

Each sync reads only the documents that changed since the previous successful sync.

- **Hash.** When the vendor publishes a `.sha256` or `.sha512` file next to a document, the document must match it.
- **Signature.** When the vendor publishes a detached `.asc` signature, it must verify. The keys come from `pgp_keys` when you set it. Otherwise the server uses the keys listed in the vendor's `provider-metadata.json` whose fingerprint matches.
- **Unsigned documents.** With `require_signature` on, a document without a valid signature is rejected. Turn it off only for a vendor that does not sign.

A rejected document is counted in `rejected_total` and its statements are not used. The other documents of the sync still are. A document that is published again replaces the statements it gave before.

A sync only fetches documents changed since the previous one, so a rejected document is not fetched again until the vendor changes it. Changing the feed's `url`, `pgp_keys` or `require_signature` makes the next sync read every document again.

Pin the vendor's keys in `pgp_keys` when you can. Then a changed provider metadata file cannot introduce a new key.

## How products are matched

The server reads the `known_not_affected` and `fixed` products of each document. Other statuses are ignored. A `fixed` statement includes the products listed as `first_fixed`.

A vendor product matches an artifact as follows:

- **By package URL.** The product's purl, with its version, equals the artifact's purl. Qualifiers and subpaths are ignored. A purl without a version matches nothing.
- **By CPE.** Only when the product has no purl. The CPE product name equals the artifact's package name, or its last `/` or `:` segment (`commons-text` of `org.apache.commons:commons-text`), ignoring case and treating `-` and `_` as the same. The CPE version equals the artifact version exactly.

A matched statement is applied only to a vulnerability the artifact's latest scan reports. The vendor's vulnerability id must equal the scan's id or one of its aliases, ignoring case. The statement is recorded under the scan's id, so a vendor `CVE-...` lands on a scan finding reported as `GHSA-...` when the scan lists the CVE as an alias.

Matching runs after every sync and once an hour, so newly scanned artifacts pick up existing statements.

## Feed statements and your own decisions

- A feed never replaces a decision an admin recorded, a decision from a [federated peer](../operations/federation.md#sharing-vex-decisions), or a statement of another feed. The first statement for an artifact and vulnerability stays.
- Recording a decision yourself replaces the feed's statement, and the feed does not take it back.
- Deleting a feed removes all statements it created.
- Disabling a feed keeps its statements.

## Where the source shows

A statement from a feed has `source_kind` `csaf`, the feed name as `source_name` and the URL of the CSAF document as `source_ref`. It appears with that source in the **VEX decisions** list of the repository, in the finding drawer and in `GET …/vex/statements`. In a generated VEX document, its status notes name the feed. See [where a decision comes from](sbom.md#where-a-decision-comes-from).

## Feed API

All routes need an admin.

| Endpoint | |
|---|---|
| `GET /api/v1/vex/feeds` | `{"feeds":[...]}` |
| `POST /api/v1/vex/feeds` | create; `201` |
| `GET /api/v1/vex/feeds/{id}` | one feed |
| `PUT /api/v1/vex/feeds/{id}` | replace the editable fields |
| `DELETE /api/v1/vex/feeds/{id}` | delete the feed and its statements; `204` |
| `POST /api/v1/vex/feeds/{id}/sync` | start a sync; `202` |

A feed also carries `cursor_at` (the newest document date seen), `last_attempt_at`, `last_success_at`, `last_error`, `documents_total`, `rejected_total`, `statements` (statements read from the vendor) and `applied` (statements recorded on your artifacts).

Creating, changing, deleting and manually syncing a feed is recorded in the audit log as `vex.feed_created`, `vex.feed_updated`, `vex.feed_deleted` and `vex.feed_synced`.

## Limits

- Only `known_not_affected` and `fixed` are imported. `known_affected` and `under_investigation` are not.
- Version ranges are not evaluated. A product matches one exact version.
- Only artifacts with a completed or partial vulnerability scan that reports vulnerabilities are matched.
- `last_error` is a short summary. The full cause is in the server log.

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| `rejected_total` grows | Documents are unsigned or signed with a key you did not pin. Check `pgp_keys`, or turn off `require_signature` for a vendor that does not sign |
| `statements` is above 0 but `applied` is 0 | No scanned artifact matches. Check that the vendor's purl has the same type, name and version as the artifact, and that the scan reports the vulnerability |
| A statement is not applied to one finding | That finding already has a decision from an admin, a peer or another feed |
