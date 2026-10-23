<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Release bundles and promotion pipelines

A release bundle is a named, versioned set of artifacts (referenced by SHA-256) that is assembled as a draft, sealed to freeze it, and archived when retired. Promotion pipelines define the ordered stages a bundle is meant to move through.

**Promotion is not implemented.** `POST .../promote` validates the request and then answers `501 not_implemented`: no artifacts are copied, no promotion record is written and the bundle's `current_stage` does not change. Everything else below works, including sealing with a GPG signature (see [Signing](#signing-a-bundle)). There is no web UI page for bundles; use the API.

All routes are under `/api/v1` and need an admin principal (`403 forbidden` otherwise). Names and versions are 1-255 bytes, valid UTF-8, without `/`, `\` or control characters.

## Lifecycle

```
draft --seal--> sealed --archive--> archived
```

| State | Allowed |
|---|---|
| `draft` | Add and remove items, delete the bundle, seal. |
| `sealed` | Frozen: no item changes, no delete. Archive; promotion is validated but returns `501`. |
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
| `POST /api/v1/bundles/{name}/{version}/promote` | Validates, then `501` (see above). |
| `GET /api/v1/bundles/{name}/{version}/promotions` | `{"promotions": [...]}`: the promotion history (always empty while promotion is unimplemented). |

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

## What the promote request checks

`POST .../promote` with `{"to_stage","target_repo","notes"}` requires a sealed bundle and, before answering `501`, validates that the bundle has a pipeline, `to_stage` is one of its stages and comes after the bundle's `current_stage`, `target_repo` (a repository name) exists, and matches the stage's `repo_id` when one is set.

## Effects on other features

- [Cleanup policies](cleanup-and-quotas.md) skip any package whose blob is referenced by a bundle item (by SHA-256), in any bundle state.
- A repository that is the source of a bundle item (or the target of a promotion record) cannot be deleted: `DELETE /api/v1/repository/{repo}` answers `409`. Delete the draft bundle or remove the item first; sealed and archived bundles cannot be deleted or edited, so such a repository stays undeletable.
