<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Docker / OCI

OCI Distribution registry at the host root (`/v2/`): push and pull images and OCI artifacts with `docker`, `podman` and other OCI clients.

| | |
|---|---|
| Format id | `docker` |
| Registry | `HOST/v2/` (image names `HOSTPORT/REPO[/image]:tag`) |
| Repository types | hosted only ([details](../repositories/hosted-proxy-group.md)) |

`HOSTPORT` below is the host and port without scheme (`localhost:8080`).

## Client setup

```sh
docker login HOSTPORT -u USERNAME -p <TOKEN>
docker tag my-image:latest HOSTPORT/REPO:latest
docker push HOSTPORT/REPO:latest
docker pull HOSTPORT/REPO:latest
```

- The username is ignored; the token is the password. The registry offers only a Basic challenge (there is no token service), so docker sends the credentials directly.
- Docker treats plain-HTTP registries other than `localhost` as insecure: serve OpenArtifact over TLS or list it under `insecure-registries` in the daemon configuration.
- Image names must be lowercase, so use lowercase repository names for Docker repositories.

## Repository and image names

The first path segment after `/v2/` is the OpenArtifact repository; the rest is the image inside it.

| Reference | Repository | Image |
|---|---|---|
| `HOSTPORT/REPO:latest` | `REPO` | default (empty) image |
| `HOSTPORT/REPO/app:1` | `REPO` | `app` |
| `HOSTPORT/REPO/org/team/app:1` | `REPO` | `org/team/app` |

- Image names follow the distribution-spec grammar (lowercase alphanumerics with `.`, `_`, `__`, `-` separators, `/` between components); otherwise `400 NAME_INVALID`. An unknown repository is `404 NAME_UNKNOWN`.
- Tags and manifests are scoped per image; blobs are scoped per repository and shared by all images in it.
- Permissions are checked against the repository, with the resource path starting with the image (`org/team/app/manifests/1`), so a [RBAC](../security/rbac.md) `path_pattern` can limit a permission to one image.
- A name may itself contain a segment called `manifests`, `blobs` or `tags`: the path is parsed from the right.

## Endpoints

| Request | Behaviour |
|---|---|
| `GET /v2/` | `200` with `Docker-Distribution-Api-Version: registry/2.0` |
| `GET\|HEAD\|PUT\|DELETE /v2/<name>/manifests/<ref>` | manifests by tag or digest |
| `GET\|HEAD\|DELETE /v2/<name>/blobs/<digest>` | blobs |
| `POST /v2/<name>/blobs/uploads/` | start an upload; `?mount=<digest>&from=<name>` attempts a cross-repository mount |
| `GET\|PATCH\|PUT\|DELETE /v2/<name>/blobs/uploads/<uuid>` | status, chunk, complete (`?digest=`), cancel |
| `GET /v2/<name>/tags/list` | tags of the image; `n` (default 100) and `last` paginate |
| `GET /v2/<name>/referrers/<digest>` | OCI index of manifests whose `subject` is the digest, with `artifactType` and `annotations` |

There is no `/v2/_catalog`. Errors use the registry JSON shape `{"errors":[{"code","message","detail"}]}`; an unsupported method is `405 UNSUPPORTED` with `Allow`.

## Push and pull behaviour

- **Blob upload:** chunks are spooled to `/tmp/openartifact/docker-uploads/<uuid>` (fixed path) and moved into the blob store on completion. A `Content-Range` that does not start at the current offset is `416 RANGE_INVALID`; a digest mismatch is `400 DIGEST_INVALID` and discards the upload. Completing an upload for a blob the repository already has succeeds with `201`.
- **Mount:** succeeds when the blob is already in the target repository, or when the caller may read the source repository and the blob is not quarantined. Otherwise the registry silently opens a normal upload session.
- **Manifest push:** stored content-addressed; `201` with `Location` and `Docker-Content-Digest`. Pushing by digest must match the body's SHA-256 (`400 MANIFEST_INVALID`). Pushing by tag moves the tag if it exists. The default media type is `application/vnd.docker.distribution.manifest.v2+json`. An OCI `subject` is recorded and echoed in `Oci-Subject`. Referenced blobs are not checked for existence and the manifest body has no size limit.
- **Delete:** a tag reference removes only that tag; the manifest and its blobs stay until [garbage collection](#garbage-collection) removes them. A digest reference removes the manifest and every tag of that image that points to it and frees the blob when nothing else references it. Blob delete removes the repository's blob record and frees storage when unreferenced. All return `202`.
- **Quarantine:** manifest and blob reads of quarantined content are refused.
- **Quota:** storage quotas are not enforced for Docker pushes, and Docker usage is not counted, so garbage collection does not touch the usage counters either ([quotas](../repositories/cleanup-and-quotas.md#storage-quotas)).

## Garbage collection

Deleting a tag, or pushing a new image to an existing tag, leaves the old manifest behind untagged, and the layers only that manifest used stay in storage. Registry garbage collection reclaims them: it deletes untagged manifests older than a grace period and then the blobs nothing references any more. [Cleanup policies](../repositories/cleanup-and-quotas.md) do not cover Docker, so this is the way to reclaim Docker storage.

**What is kept.** A manifest is deleted only when it is older than the grace period (its last push or re-push) and none of these holds:

- a tag of the same image points at it;
- it is a child of a kept index or manifest list (a multi-arch image keeps its per-platform manifests);
- it is the `subject` of a kept manifest, or a referrer (signature, SBOM, attestation) of a kept subject: the pair lives and dies together;
- it is tied to a manifest in one of the previous ways, directly or through several steps.

After the manifests, a blob record of the repository is deleted when no remaining manifest of the repository references it **and** no client has used it within the grace period. A blob counts as used when it is uploaded, re-pushed, mounted or probed with `HEAD`. The storage object is removed only when no other repository (or format) still references the same content. Layers uploaded but never referenced by a manifest, for example by an aborted push, are collected the same way once they are older than the grace period.

**Schedule.** A background worker collects every Docker repository each `OPENARTIFACT_DOCKER_GC_INTERVAL` (default `1h`; `0` disables the schedule) with `OPENARTIFACT_DOCKER_GC_GRACE_PERIOD` (default `24h`). See [configuration](../getting-started/configuration.md#workers-workers).

**On demand.** An admin can run it for one repository, synchronously, and preview it first:

```sh
# what would be deleted, using the configured grace period
curl -X POST HOST/api/v1/repositories/REPO/registry-gc \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"dry_run":true}'

# delete now, ignoring the grace period
curl -X POST HOST/api/v1/repositories/REPO/registry-gc \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"grace_period_seconds":0}'
```

Both body fields are optional (an empty body is a real run with the configured grace period): `dry_run` and `grace_period_seconds` (`0` means no grace; negative values are `400`). The response reports `manifests_deleted`, `blob_refs_deleted` (blob records of this repository), `blobs_freed` and `bytes_freed` (storage objects actually removed; in a dry run an upper bound that ignores sharing with other repositories), `failures` (storage deletions that failed and were logged; the orphan collector retries them), and the `manifests` and `blobs` involved, capped at 1000 entries each with `truncated` set when capped. `409` means a run is already in progress for the repository, `400` that the repository is not a Docker repository.

**Safety.**

- A run holds a Postgres advisory lock on a pinned connection, so runs on one repository never overlap (across replicas too).
- Every deletion re-checks its conditions (still old enough, still untagged, still not tied to a kept manifest or referenced by one) inside a transaction that holds a per-repository lock which manifest pushes also take, so a push is either fully visible to the collector or happens after it. Blob storage is released through the same reference-counted path as every other delete.
- The grace period is what protects a push in progress: layers are uploaded before the manifest that references them, and a tag is set after the manifest is stored. Keep it longer than your slowest push. With `grace_period_seconds: 0` a push running at that moment can lose layers or its manifest; use it on a quiet repository.
- Manifests pushed before this feature existed (or restored from a backup) are indexed on the first run, which reads each of their bodies from the blob store once. Blob records that existed at upgrade count as used at the time of the upgrade, so old unreferenced layers become collectable one grace period later.

**Consequences.** A reference by digest to an untagged manifest (`image@sha256:...`) stops resolving once the grace period has passed; pin by tag, or keep a tag on anything that must stay pullable. Digests of kept manifests are unaffected.

## Signing

The registry does not verify image signatures; signature artifacts are stored like any other manifest and are listed through `referrers` when they carry a `subject`. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).
