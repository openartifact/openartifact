<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Trash: soft delete and deletion protection

Deleting an artifact, a version, a Docker tag or a whole repository does not destroy it. The item moves to the trash, disappears for every client, and stays restorable for a retention period (7 days by default). A background worker then removes it for good. Repositories can opt out of the delay, or turn on `immutable_releases` to protect released versions.

## What is soft-deleted

Everything a delete can remove, in every format, plus repositories:

- Package rows of the hosted formats: Maven, Ivy, sbt, npm versions, PyPI, NuGet, Go modules, Cargo, RubyGems, Conda, Conan, Helm, raw files, Docker manifests and tags, RPM and Debian packages.
- Whole repositories.
- Items removed by a [cleanup policy](../repositories/cleanup-and-quotas.md#cleanup-policies).

Deleting a Docker manifest takes every tag that points to it into the trash in the same step, and restoring the manifest brings those tags back.

A trashed item is not copied or renamed: it is the same record with a `deleted_at` timestamp, and its blob stays where it is. Everything except the trash itself behaves as if the item did not exist:

| Area | Behavior while an item is in the trash |
|---|---|
| Native protocols | `404`, like any missing path or version |
| Listings, UI, search, counts and statistics | the item is absent |
| Generated indexes (Debian `Packages`, RPM `repodata`, Helm `index.yaml`, npm packuments, Maven `maven-metadata.xml`, PyPI index, ...) | rebuilt without the item; metadata is regenerated on delete and again on restore |
| Proxy and group repositories | a trashed hosted item is not served through a group; a trashed repository is not a group member |
| Uniqueness | the path or version can be published again while the old copy is in the trash (see [restore conflicts](#restore)) |
| Blobs | kept until purge; storage usage does not drop on delete |
| Quota | trashed bytes keep counting ([below](#quota)) |
| Authorization cache | see [below](#multiple-replicas-and-the-auth-cache) |

### Multiple replicas and the auth cache

Repository, project and membership lookups made while authorizing a request are cached in memory for `DB_AUTH_CACHE_TTL_MS` (default 2000 ms). Trashing or restoring a repository through a replica invalidates that replica's cache at once, but the other replicas keep their cached copy until the TTL expires. For up to that long, another replica can still serve a just-trashed repository (and keep refusing a just-restored one). The same applies to changes made directly in the database. Lower the TTL, or set it to `0`, if that window is not acceptable; see also [RBAC](../security/rbac.md).

## Retention

| Setting | Environment variable | YAML key | Default |
|---|---|---|---|
| Server-wide retention in days | `OPENARTIFACT_TRASH_RETENTION` | `trash.retentionDays` | `7` |
| Pause between purge passes | `OPENARTIFACT_TRASH_PURGE_INTERVAL` | `trash.purgeInterval` | `15m` |

The full rows are in [configuration](../getting-started/configuration.md#trash-trash). Setting the retention to `0` turns the trash off: deletes are immediate and permanent, as before the feature existed. `trash.purgeInterval: 0` disables the scheduled purge (items then stay until an admin purges them by hand).

There is deliberately no `OPENARTIFACT_REPO_DELETE_DELAY`. The trash window is the deletion delay: a deleted repository is hidden at once, can be restored for the retention period and is removed afterwards.

### Per-repository override

Every repository has a `trash_retention_days` setting that applies to deletes of its content and to deleting the repository itself:

| Value | Meaning |
|---|---|
| `null` or omitted | use the server-wide retention |
| `N >= 1` | keep deleted items `N` days |
| `0` | delete immediately and permanently (the behavior before the trash) |

```sh
curl -X PATCH HOST/api/v1/repository/builds -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"trash_retention_days":30}'

# CI scratch repository: no safety net, free space at once
curl -X PATCH HOST/api/v1/repository/ci-scratch -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"trash_retention_days":0}'
```

Also available at creation (`POST /api/v1/repositories`), in the UI repository settings and in the CLI: `oa repo create|update --trash-retention-days N` (`default` on `update` goes back to the server-wide value). Negative values are `400`.

## Kinds

Trash items are addressed by `kind` and `id` (both shown by the list endpoints).

| Kind | What it is |
|---|---|
| `repository` | a whole repository |
| `maven_artifact`, `ivy_artifact`, `sbt_artifact` | a file in a Maven, Ivy or sbt repository |
| `npm_version` | one npm package version |
| `pypi_package` | a PyPI distribution file |
| `nuget_package` | a NuGet package version |
| `go_module` | a Go module version |
| `cargo_crate` | a Cargo crate version |
| `rubygems_gem` | a gem version |
| `conda_package` | a Conda package |
| `conan_package` | one stored file of a Conan recipe or package revision |
| `helm_chart` | a Helm chart version |
| `raw_artifact` | a raw file |
| `docker_manifest` | a Docker/OCI manifest (and its tags) |
| `docker_tag` | a single Docker tag |
| `rpm_package` | an RPM package |
| `deb_package` | a Debian package |

## API

JSON is snake_case. An item:

```json
{
  "id": "6f0c2b9e-...",
  "kind": "maven_artifact",
  "repository_id": "b1d1...",
  "repository": "releases",
  "format": "maven",
  "name": "com/acme/app/1.4.0/app-1.4.0.jar",
  "version": "1.4.0",
  "size_bytes": 1048576,
  "deleted_at": "2026-01-02T15:04:05Z",
  "deleted_by": "alice",
  "purge_at": "2026-01-09T15:04:05Z"
}
```

A list is `{"items":[...],"total":N,"page":P,"limit":L}` (`page` from 1).

| Method and path | Who | Purpose |
|---|---|---|
| `GET /api/v1/trash` | admin | All trashed items, trashed repositories included. Query `page`, `limit`, `kind`, `repository` |
| `GET /api/v1/repositories/{name}/trash` | `delete` on the repository | One repository's trashed items. Query `page`, `limit`, `kind` |
| `POST /api/v1/trash/{kind}/{id}/restore` | admin | Restore any item or a repository; `200` with the item |
| `POST /api/v1/repositories/{name}/trash/{kind}/{id}/restore` | `delete` on the repository | Restore an item of that repository (not for kind `repository`) |
| `DELETE /api/v1/trash/{kind}/{id}` | admin | Purge now, permanently; `204` |
| `DELETE /api/v1/repositories/{name}/trash` | `manage` on the repository (or admin) | Empty the repository's trash: purge all its trashed items now. `200` `{"purged": N, "remaining": M}` |

```sh
HOST=http://localhost:8080

# what is in the trash (admin)
curl -sS -H "Authorization: Bearer <TOKEN>" "$HOST/api/v1/trash?kind=maven_artifact&limit=50"

# one repository's trash, as a user who can delete there
curl -sS -H "Authorization: Bearer <TOKEN>" "$HOST/api/v1/repositories/releases/trash"

# restore an item
curl -sS -X POST -H "Authorization: Bearer <TOKEN>" \
  "$HOST/api/v1/repositories/releases/trash/maven_artifact/<ID>/restore"

# restore a deleted repository (admin)
curl -sS -X POST -H "Authorization: Bearer <TOKEN>" "$HOST/api/v1/trash/repository/<ID>/restore"

# purge now
curl -sS -X DELETE -H "Authorization: Bearer <TOKEN>" "$HOST/api/v1/trash/docker_tag/<ID>"
```

Errors: `404` for an unknown or no longer trashed item, `409` for a restore conflict (`trash_conflict`, or `repository_in_trash` when the item's repository is itself in the trash; restore the repository first). Permissions: [RBAC](../security/rbac.md).

## UI

Repository pages have a Trash tab listing that repository's deleted items with who deleted them, when, and when they will be purged, with Restore for users who can delete there. Admins also get a global Trash page (items and deleted repositories) with Restore and Purge now. Deleting a repository asks for its name, the same as the API and CLI below.

## CLI

```sh
oa trash list                                    # everything (admin)
oa trash list --repo releases --kind maven_artifact
oa trash restore maven_artifact <id> --repo releases
oa trash restore repository <id>                 # admin
oa trash purge docker_tag <id> --yes             # admin; prompts without --yes
oa trash purge --repo releases --all --yes       # empty a repository's trash (repository manage permission)
oa repo delete old-repo --confirm old-repo
```

Details and flags: [CLI](../integrations/cli.md#trash).

## Repositories

`DELETE /api/v1/repository/{name}` is the most destructive call of the API, so it needs the repository name as confirmation, as a query parameter or a JSON body:

```sh
curl -X DELETE "$HOST/api/v1/repository/old-repo?confirm=old-repo" -H "Authorization: Bearer <TOKEN>"
curl -X DELETE "$HOST/api/v1/repository/old-repo" -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"confirm":"old-repo"}'
```

A missing or wrong `confirm` is `400 confirmation_required`; the `oa` CLI prompts for it on a terminal and takes `--confirm <name>` in scripts.

What happens next:

- The repository is hidden at once: its native URLs, listings, search results, group memberships and API routes answer `404`. Its packages are not touched individually; they stay as they are and are hidden along with the repository.
- Its **name stays reserved** while it is in the trash. Creating a repository with the same name is `409`, until the old one is purged or restored.
- An admin can restore it from the trash (`kind` `repository`) within the retention period; everything comes back as it was, including its packages and settings. After the retention (or immediately when the repository's `trash_retention_days` is `0`) the purge removes the repository, its packages and its quota counters.
- Items that were trashed individually before the repository keep the retention window they had left at that moment: a restore shifts their deletion time by the time the repository spent in the trash.
- A project cannot be deleted (`409`) while it owns repositories, repositories in the trash included; purge or restore them first.
- The existing guard still applies: the delete is `409` while a release bundle or promotion references the repository.

## Restore

Restoring returns the item with identical content and metadata, and the generated indexes are rebuilt to include it again.

A restore can conflict:

- **The path or version was published again meanwhile.** Because uniqueness only applies to live rows, you may re-publish the same coordinates while the old copy is in the trash. Restoring the old copy then answers `409 trash_conflict`. Delete or move the new one first, or purge the old one.
- **The repository is in the trash** (`409 repository_in_trash`): restore the repository first.
- **A docker tag's manifest is in the trash** (`409 manifest_in_trash`): restore the manifest first. Restoring a manifest also restores the tags that were trashed with it.

**Docker blobs of trashed images.** While an image is in the trash its manifest and tags are `404`, and so are the layers and config blobs only trashed manifests reference (`404 BLOB_UNKNOWN` on `GET`/`HEAD` by digest); a layer a live image shares keeps being served, and restoring the manifest makes its blobs fetchable again. The blobs are deleted only once the manifest is purged and garbage collection runs. See [Docker](../formats/docker.md#garbage-collection).

## Purge

The purge worker runs every `OPENARTIFACT_TRASH_PURGE_INTERVAL` (default `15m`) and permanently deletes items whose retention has expired, repositories included. An admin can purge any item immediately with `DELETE /api/v1/trash/{kind}/{id}` (UI: Purge now; CLI: `oa trash purge`).

### Empty a repository's trash

`DELETE /api/v1/repositories/{name}/trash` purges every trashed item of one repository now (UI: Empty trash on the repository's trash page; CLI: `oa trash purge --repo NAME --all`). It takes the same steps as a single purge for each item: the record is removed, blobs are freed through the reference-counted path, the [quota](#quota) is released, an `artifact.purged` audit entry is written and the webhook event is emitted. It needs the repository-scoped `manage` permission (global admins have it everywhere), because it is destructive in bulk. Only items are purged: the repository itself, and a repository that is itself in the trash (`404`), are not touched. A request purges at most 1000 items and answers `{"purged": N, "remaining": M}`; repeat it while `remaining` is above 0 (the CLI does this itself).

Purging removes the record, releases the repository's [quota](#quota) (for npm it also prunes dist-tags and packages left with no versions), and frees blobs through the usual reference-counted path. Blobs are content-addressed and deduplicated: a blob is removed from storage only when no other row references it, including rows of other repositories and other formats, and including other items still in the trash. Purging one copy of a shared file therefore frees nothing.

## Quota

Soft-deleted bytes keep counting against the repository's [quota](../repositories/cleanup-and-quotas.md#trash-and-quotas) until purge: the delete does not release the reservation, and the quota reconcile counts trashed rows. The purge releases it. Quota errors say so: uploads refused over quota answer `507` with `repository storage quota exceeded (bytes of trashed items count until purged; see the repository trash)`. If you delete to make room, either [empty the repository's trash](#empty-a-repositorys-trash), purge the items or set the repository's `trash_retention_days` to `0`. Restoring needs no new reservation.

## Immutable releases

`immutable_releases` is a per-repository setting (`POST /api/v1/repositories`, `PATCH /api/v1/repository/{name}`, UI settings, `oa repo ... --immutable-releases`), off by default. When on, a **released** version can be neither deleted nor overwritten: the request is `409`, in the format's own error style. Admins can override one request with `?force=true`; for anyone else the parameter is ignored. Pre-releases and snapshots stay mutable, so CI can keep publishing over them.

What counts as a release:

| Format | A version is a release when |
|---|---|
| Maven, Ivy, sbt | it is not a `-SNAPSHOT` version |
| npm, NuGet, Cargo, Helm | it is semver without a pre-release part (`1.2.3` yes, `1.2.3-rc.1` no) |
| Go | semver without a pre-release part; pseudo-versions (`v0.0.0-2026...-abcdef123456`) count as pre-releases |
| PyPI | PEP 440 without an `a`, `b`, `rc` or `dev` segment (`1.0`, `1.0.post1` are releases; `1.0rc1`, `1.0.dev3` are not) |
| RubyGems | the version has no letters (`1.2.3` yes, `1.2.3.pre` no) |
| Raw, Debian, RPM, Conda, Conan, Docker | every version is a release; there is no snapshot concept |

The rule covers both directions:

- **Delete** of a released version (including a Docker tag or manifest) is refused.
- **Overwrite** of an existing release is refused where the format allows overwriting at all: Maven re-deploys, raw `PUT` over an existing path, Helm and others that replace content, and Docker re-tagging a tag to a different digest. Formats that already answer `409` for a duplicate keep doing so; pushing the same Docker digest again is not a change.
- [Cleanup policies](../repositories/cleanup-and-quotas.md#cleanup-policies) skip released versions of such repositories.

```sh
# refused
curl -X DELETE -H "Authorization: Bearer <TOKEN>" "$HOST/repos/maven/releases/com/acme/app/1.4.0/app-1.4.0.jar"   # 409

# admin override
curl -X DELETE -H "Authorization: Bearer <ADMIN_TOKEN>" "$HOST/repos/maven/releases/com/acme/app/1.4.0/app-1.4.0.jar?force=true"
```

A raw file uploaded through a presigned URL is guarded when the upload is **completed**, not when it is created: `POST /api/v1/repositories/{name}/uploads/{id}/complete` over an existing path answers `409` with code `immutable_release` and records nothing. The upload stays pending (the cleanup worker removes it after it expires), so an administrator can complete it with `?force=true`; a plain retry is refused again.

## Audit and webhook events

| Event | Audit | Webhook | When |
|---|---|---|---|
| `artifact.deleted` | yes, details `moved to trash` or `deleted` | yes | any delete, soft or permanent |
| `artifact.restored` | yes | yes | an item is restored |
| `artifact.purged` | yes | yes | an item is purged, by an admin or by the worker |
| `repository.deleted` | yes | yes | a repository is deleted (moved to the trash, or permanently with retention 0) |
| `repository.restored` | yes | yes | a repository is restored |
| `repository.purged` | yes | yes | a repository is purged |

Payloads keep the existing shapes: the artifact events carry the artifact payload, the repository events the repository payload ([webhooks](webhooks.md#payloads)). Subscribe to `artifact.deleted` and `repository.deleted` knowing they now also mean "moved to the trash". Audit entries are listed in [monitoring](monitoring.md#audit-log).

## Backups

[Backups](backup.md) do not include trashed repositories or package rows (they are exported through the queries clients use, which hide the trash), so a restore brings back live data only; restore what you need from the trash before taking a backup. The per-repository `trash_retention_days` and `immutable_releases` settings are part of the repository records.

## Related

- [Cleanup policies and quotas](../repositories/cleanup-and-quotas.md): policies move versions to the trash; quota accounting.
- [Docker garbage collection](../formats/docker.md#garbage-collection): trashed manifests and their layers are not collected.
- [Upgrading](../getting-started/upgrading.md#050-unreleased): behavior changes and migrations `017`/`018` (locking, index builds).
