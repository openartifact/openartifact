<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Release bundles and promotion pipelines

A release bundle is a named, versioned set of artifacts (referenced by SHA-256) that is assembled as a draft, sealed to freeze it, and archived when retired. Promotion pipelines define the ordered stages a bundle is meant to move through.

Promoting a sealed bundle copies its artifacts into the repository of the next pipeline stage ([Promoting a bundle](#promoting-a-bundle)). Sealing can add a GPG signature (see [Signing](#signing-a-bundle)). Everything is also available in the [web UI](#web-ui).

All routes are under `/api/v1` and need an admin principal (`403 forbidden` otherwise). Names and versions are 1-255 bytes, valid UTF-8, without `/`, `\` or control characters.

## Web UI

Administrators find **Release bundles** in the sidebar (`/bundles`).

- **List**: filter by name and state; **New bundle** creates a draft (name, version, description, optional pipeline); **New pipeline** defines the ordered stages, each optionally bound to a repository.
- **Detail** (`/bundles/{name}/{version}`): summary, the pipeline as a stage trail with the bundle's current stage, the items (format, SHA-256, size, source repository), the vulnerability scan totals of the source repositories (linking to the filtered [vulnerability scans](../security/scanning.md)) and the promotion history.
- **Draft**: *Add items* (source repository, SHA-256, artifact path, optional format and size), remove items, *Seal* and *Delete*. Sealing offers the registered GPG keys; choosing one signs the manifest.
- **Sealed**: *Promote* (target stage among the stages after the current one, target repository, notes; a stage bound to a repository fixes the target; *Replace conflicting content (force)* sends `?force=true`, see [Promoting a bundle](#promoting-a-bundle)), *Archive*, and downloads of the *Manifest* and, for a signed bundle, the *Signature*.

## Lifecycle

```
draft --seal--> sealed --archive--> archived
```

| State | Allowed |
|---|---|
| `draft` | Add and remove items, delete the bundle, seal. |
| `sealed` | Frozen: no item changes, no delete. Archive, or promote through the pipeline stages. |
| `archived` | Read only. |

An operation in the wrong state is `409 conflict`. Sealing an empty bundle is `400 validation_failed`. A bundle is unique by `(name, version)` (`409` on duplicates).

## Bundles API

| Method and path | Purpose |
|---|---|
| `GET /api/v1/bundles?name=&state=&page=&limit=` | List (`limit` 1-100, default 25) as `{total, page, limit, bundles}`. |
| `POST /api/v1/bundles` | Create a draft: `{"name","version","description","pipeline_id"}` (`pipeline_id` optional, must be an existing pipeline's UUID). `201`. |
| `GET /api/v1/bundles/{name}/{version}` | One bundle: `id, name, version, description, state, pipeline_id, current_stage, created_by, sealed_at, sealed_by, archived_at, archived_by, gpg_key_id, gpg_signature, created_at, updated_at`. |
| `DELETE /api/v1/bundles/{name}/{version}` | Delete; draft only. `204`. |
| `GET /api/v1/bundles/{name}/{version}/items` | `{"items": [...]}`. |
| `POST /api/v1/bundles/{name}/{version}/items` | Add 1-500 items atomically (draft only); `201` with the stored items. |
| `DELETE /api/v1/bundles/{name}/{version}/items/{sha256}` | Remove one item (draft only). `204`. |
| `POST /api/v1/bundles/{name}/{version}/seal` | Draft to sealed; records `sealed_at`/`sealed_by`. Optional body `{"gpg_key_id"}` signs the manifest. |
| `GET /api/v1/bundles/{name}/{version}/manifest` | The canonical JSON manifest a signature covers (admin). |
| `GET /api/v1/bundles/{name}/{version}/manifest.asc` | The armored detached signature (admin); `404` when the bundle was sealed without a key. |
| `POST /api/v1/bundles/{name}/{version}/archive` | Sealed to archived; records `archived_at`/`archived_by`. |
| `POST /api/v1/bundles/{name}/{version}/promote` | Copy the bundle into a stage's repository ([details](#promoting-a-bundle)). |
| `GET /api/v1/bundles/{name}/{version}/promotions` | `{"promotions": [...]}`: the promotion history, newest first. |

Each item:

| Field | Required | Notes |
|---|---|---|
| `source_repo_id` | yes | UUID of an existing repository (the `id` of `GET /api/v1/repository/{repo}`). |
| `sha256` | yes | 64 hex characters (case-insensitive, stored lowercase); unique within the bundle. |
| `artifact_path` | yes | Path of the artifact (at most 1024 bytes). |
| `format` | no | Defaults to the source repository's format. |
| `size_bytes` | no | Non-negative. |
| `component_id` | no | Optional id stored with the item. |

The server checks that the source repository exists but does not verify that the digest is stored there.

```sh
curl -X POST HOST/api/v1/bundles -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"shop","version":"2.4.0","description":"Release 2.4.0"}'

curl -X POST HOST/api/v1/bundles/shop/2.4.0/items -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"items":[{"source_repo_id":"<REPO_UUID>","sha256":"<SHA256>",
                 "artifact_path":"com/acme/shop/2.4.0/shop-2.4.0.jar","size_bytes":1048576}]}'

curl -X POST HOST/api/v1/bundles/shop/2.4.0/seal -H "Authorization: Bearer <TOKEN>"
```

### Signing a bundle

`POST .../seal` with `{"gpg_key_id": "<key id | fingerprint | config>"}` (a key from [`GET /api/v1/admin/gpg/keys`](../security/signing.md#managing-keys); `config` is the `gpg.keyPath` key) signs the bundle manifest before sealing. The manifest is deterministic JSON (`schema: openartifact.release-bundle/v1`, name, version, description, items ordered by SHA-256), so it can be re-fetched and verified at any time:

```sh
curl -X POST HOST/api/v1/bundles/shop/2.4.0/seal -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"gpg_key_id":"<KEY_ID>"}'
curl -H "Authorization: Bearer <TOKEN>" -o manifest.json     HOST/api/v1/bundles/shop/2.4.0/manifest
curl -H "Authorization: Bearer <TOKEN>" -o manifest.json.asc HOST/api/v1/bundles/shop/2.4.0/manifest.asc
gpg --verify manifest.json.asc manifest.json
```

The bundle's `gpg_key_id` is the signer's fingerprint (it stays meaningful after the key is deleted) and `gpg_signature` the armored signature. An unknown key answers `400 validation_failed` and leaves the bundle a draft. The seal fails with `409` if an item changed while the manifest was being signed.

## Pipelines API

A pipeline is a named, ordered list of stages; each stage has a `name` and an optional `repo_id` (UUID of an existing repository that the stage must promote into).

| Method and path | Purpose |
|---|---|
| `GET /api/v1/pipelines` | `{"pipelines": [...]}`. |
| `POST /api/v1/pipelines` | `{"name","description","stages":[{"name":"qa","repo_id":"<UUID>"},{"name":"prod"}]}`; at most 50 stages, unique stage names. `409` if the name exists. |
| `GET /api/v1/pipelines/{name}` | One pipeline. |
| `PATCH /api/v1/pipelines/{name}` | Update `description` and/or replace `stages`. |
| `DELETE /api/v1/pipelines/{name}` | Delete (bundles referencing it keep existing, with no pipeline). `204`. |

## Promoting a bundle

`POST /api/v1/bundles/{name}/{version}/promote` with `{"to_stage","target_repo","notes"}` copies every item of a sealed bundle into `target_repo`.

Checks before anything is copied: the bundle is sealed and has a pipeline, `to_stage` is one of its stages and comes after the bundle's `current_stage`, `target_repo` (a repository name) exists and matches the stage's `repo_id` when one is set. The target must be a hosted repository (not a proxy or group), not deleted, of the same format as every item's source repository, and different from it.

A promotion is one database transaction. Either every item is copied, the `bundle_promotions` record is written and `current_stage` moves to `to_stage`, or nothing changes at all: a failed promotion leaves no rows, no quota charge and no history entry, and can simply be retried. Concurrent promotions of the same bundle are serialised; the loser gets `409 conflict`.

### What a copy is

Artifact bytes are content-addressed and stored once. Promotion adds metadata rows in the target that reference the **same blob**; no bytes are read, written or duplicated, and the cost of a promotion does not depend on artifact size. Each item is located in its source repository by SHA-256 (`artifact_path` only narrows the match when several artifacts share the content) and the whole logical artifact is copied, so the target can serve it exactly like a direct upload:

| Format | What is copied for an item |
|---|---|
| maven, ivy, sbt, pypi, nuget, cargo, rubygems, conda, helm, raw | The artifact row (file, coordinates, metadata). |
| npm | The version (with its manifest) and the source dist-tags that point at it; the target package is created if missing, gets a `latest` tag when it has none, and its packument is regenerated. |
| go | The module version: zip, `go.mod` and `.info`. The item may be the zip or the `go.mod` blob. |
| conan | The whole recipe revision the file belongs to: recipe files and every binary package revision under it. The copy becomes the newest revision in the target. |
| docker | The manifest, its config and layer references, the manifest's tags and, for an image index, every child manifest. Blobs the target already holds are not charged again. |
| deb | The package, plus the distribution, component and architecture rows it needs; the Packages/Release indexes are regenerated. |
| rpm | The package; the repository's metadata is marked stale and regenerated. Signing keys of the source repository are never copied. |

Indexes that clients read from the database (maven, pypi, nuget, cargo, rubygems, conda, helm, go, conan, docker) need no regeneration; the formats with generated index files (deb, rpm, maven metadata, npm packuments) are refreshed by their background workers right after the promotion commits.

### Rules applied in the target

- **Conflicts.** Promotion never overwrites. An item already present with the same content is skipped, so re-promoting is safe (`existing` in the response). The same coordinates with different content answer `409 artifact_conflict`; when the target has [immutable releases](../operations/trash.md#immutable-releases) and the version is a release, `409 immutable_release`. An administrator can send `?force=true` to replace such content, including released versions of an immutable repository (the same override as for [deletes and overwrites](../operations/trash.md#immutable-releases)): the conflicting target rows are moved to the target's [trash](../operations/trash.md), restorable until retention expires, and the promoted ones take their place. They are listed in `replaced` in the response, and counted in the audit entry and the webhook.
- **Trash.** Items in the target's trash do not count: a live copy is created next to them.
- **Quota.** The new artifacts and bytes are charged to the target repository's [quota](cleanup-and-quotas.md); `507 quota_exceeded` if they do not fit. Existing items and blobs already referenced by the target are not charged.
- **License policy.** The target's [license policies](../security/scanning.md#blocking-licenses) are evaluated against the copied packages; a `block` policy gives `403 LICENSE_BLOCKED`.
- **Quarantine.** An item that is quarantined in its source repository is refused with `403 ARTIFACT_QUARANTINED`.
- A source artifact that no longer exists (deleted, trashed or purged since the bundle was built) is `409 artifact_missing`.

A docker image whose layer references are not indexed yet (registry GC records them), or whose tag changed during the promotion, is `409 conflict`: retry later. The error body names the offending item (`sha256` and path). Unsupported or mismatched formats are `400 validation_failed`.

### Response and side effects

`200` with `{"promotion": {...}, "copied": N, "existing": N, "replaced": N, "items": [...]}`. `copied` and `existing` count artifacts (one item can be several, for instance an npm version or a docker image); each entry of `items` has `sha256`, `artifact_path`, `format` and the `copied`, `existing` and `replaced` artifact lists (`name`, `version`, `path`, `sha256`, `size`). The same record is listed by `GET .../promotions` (`status: success`, `completed_at`, `item_count`, `notes`).

A successful promotion is written to the [audit log](../operations/monitoring.md) as `bundle.promoted` (repository: the target) and emits the [`bundle.promoted` webhook event](../operations/webhooks.md) to the target repository's and the global webhooks.

```sh
curl -X POST HOST/api/v1/bundles/shop/2.4.0/promote -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"to_stage":"prod","target_repo":"releases","notes":"Release 2.4.0"}'
```

## Effects on other features

- [Cleanup policies](cleanup-and-quotas.md) skip any package whose blob is referenced by a bundle item (by SHA-256), in any bundle state.
- A repository that is the source of a bundle item (or the target of a promotion record) cannot be deleted: `DELETE /api/v1/repository/{repo}` answers `409`. Delete the draft bundle or remove the item first; sealed and archived bundles cannot be deleted or edited, so such a repository stays undeletable.
