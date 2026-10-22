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
| Repository types | hosted, proxy, group ([details](../repositories/hosted-proxy-group.md)) |

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
- Tags and manifests are scoped per image. Blobs are stored once per repository, but each image only sees the blobs it was pushed, mounted or referenced by one of its manifests: a `GET`/`HEAD`/`DELETE` of another image's digest is `404 BLOB_UNKNOWN`, and a manifest referencing it is `400 MANIFEST_BLOB_UNKNOWN`. An image that needs a layer another image of the repository holds gets it by pushing the same bytes (the server stores them once) or by a mount with `from=<repo>/<other image>`, which needs read permission on the source image.
- Permissions are checked against the repository, with the resource path starting with the image (`org/team/app/manifests/1`), so a [RBAC](../security/rbac.md) `path_pattern` can limit a permission to one image, blobs included. Blobs that already existed when image links were introduced (migration `022`) stay visible to the images whose manifests reference them; a blob no manifest references yet stays visible to every image of its repository until an image claims it.
- A name may itself contain a segment called `manifests`, `blobs` or `tags`: the path is parsed from the right.

## Endpoints

| Request | Behaviour |
|---|---|
| `GET /v2/` | `200` with `Docker-Distribution-Api-Version: registry/2.0` |
| `GET\|HEAD\|PUT\|DELETE /v2/<name>/manifests/<ref>` | manifests by tag or digest |
| `GET\|HEAD\|DELETE /v2/<name>/blobs/<digest>` | blobs |
| `POST /v2/<name>/blobs/uploads/` | start an upload; `?mount=<digest>&from=<name>` attempts a cross-repository mount |
| `GET\|PATCH\|PUT\|DELETE /v2/<name>/blobs/uploads/<uuid>` | status, chunk, complete (`?digest=`), cancel |
| `GET /v2/<name>/tags/list` | tags of the image; `n` (default 100, at most 1000) and `last` paginate; a `Link: <...>; rel="next"` header is sent when more tags exist, `n=0` returns an empty list, an invalid `n` is `400 PAGINATION_NUMBER_INVALID` |
| `GET /v2/<name>/referrers/<digest>` | OCI index of manifests whose `subject` is the digest, with `artifactType` and `annotations` |

There is no `/v2/_catalog`. Errors use the registry JSON shape `{"errors":[{"code","message","detail"}]}`; an unsupported method is `405 UNSUPPORTED` with `Allow`.

## Push and pull behaviour

- **Blob upload:** chunks are spooled to `<workers.dockerUploadDir>/<uuid>` (default `/tmp/openartifact/docker-uploads`) and moved into the blob store on completion. A `Content-Range` that does not start at the current offset is `416 RANGE_INVALID`; a staged file shorter than the recorded offset is `404 BLOB_UPLOAD_UNKNOWN`, and stray bytes beyond it are discarded; sessions idle longer than `workers.dockerUploadTtl` (default 24h) are swept with their files; a digest mismatch is `400 DIGEST_INVALID` and discards the upload. Requests on one session run one at a time (a concurrent `PATCH` waits for a completing `PUT` and then finds the session gone), and the blob store re-hashes the bytes it receives, so the stored object is always the one whose digest was verified. Completing an upload for a blob the repository already has succeeds with `201`.
- **Mount:** succeeds when the blob is already available to the target image, or when the caller may read the source repository and the blob is not quarantined. Otherwise the registry silently opens a normal upload session.
- **Manifest push:** stored content-addressed; `201` with `Location` and `Docker-Content-Digest`. Pushing by digest must match the body's SHA-256 (`400 MANIFEST_INVALID`). Pushing by tag moves the tag if it exists. The default media type is `application/vnd.docker.distribution.manifest.v2+json`. An OCI `subject` is recorded and echoed in `Oci-Subject`. Every referenced config and layer blob must be available to the image (checked again under the registry GC lock); the manifest body is limited to 4 MiB (`413 SIZE_INVALID`).
- **Delete:** a tag reference removes only that tag; the manifest and its blobs stay until [garbage collection](#garbage-collection) removes them. A digest reference removes the manifest and every tag of that image that points to it and frees the blob when nothing else references it. Blob delete removes the repository's blob record and frees storage when unreferenced. All return `202`. With the default [trash](../operations/trash.md) retention, a tag or manifest delete moves the item to the trash instead (a manifest takes its tags with it, and its blobs stay referenced until purge); the client sees the same `202` and a following pull is `404`.
- **Immutable releases:** in a repository with `immutable_releases` every tag is a release. Moving a tag to a different digest, deleting a tag and deleting a manifest answer `409` unless an admin sends `?force=true`; re-pushing the same digest is not a change. See [immutable releases](../operations/trash.md#immutable-releases).
- **Quarantine:** manifest and blob reads of quarantined content are refused.
- **Quota:** pushes are checked against the repository's [storage quota](../repositories/cleanup-and-quotas.md#what-counts): blob bytes (layers and configs, once per repository) and one artifact per manifest. A push over quota is refused with `507` and the OCI `DENIED` error. Manifest delete, blob delete and garbage collection give the usage back.

## Proxy and group repositories

A docker **proxy** pulls through from an upstream registry (default Docker Hub); a docker **group** serves the first member that has an image. Both are read-only and live under `/v2/` only.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"dockerhub","format":"docker","repo_type":"proxy"}'
docker pull HOSTPORT/dockerhub/library/alpine:3.20
docker pull HOSTPORT/dockerhub/alpine:3.20            # same image on Docker Hub
```

- **Upstream auth:** the proxy answers the upstream's `WWW-Authenticate` challenge itself: Bearer (anonymous token, or Basic `upstream_username`/`upstream_password` sent to the token service with the challenged `scope`) and Basic. The stored credentials go only to a token realm on the upstream's own origin or an `https` realm on the same registrable domain (Docker Hub's `auth.docker.io` for `registry-1.docker.io`); any other realm is asked anonymously and a warning is logged. The token cache is keyed by the credentials too, so changing the password never reuses a token minted with the old one. Tokens are cached in memory per image until shortly before they expire. The token realm passes the same SSRF validation as the upstream URL.
- **Verification:** every manifest and blob is spooled to disk and its sha256/sha512 checked against the requested digest and the upstream's `Docker-Content-Digest` before it is cached or served. A mismatch is `502 UNAVAILABLE` and nothing is cached.
- **Caching:** manifests and blobs addressed by digest are immutable and never expire. A tag is served from cache for `cache_ttl_seconds`; afterwards it is revalidated with a `HEAD` and refetched only when the digest changed. When the upstream is unreachable, rate-limited (429) or answers 5xx, the expired tag is served stale for up to `max_stale_seconds` (`Warning: 110`), then `502`. An upstream 404 is cached for `negative_cache_ttl_seconds`; an upstream 401/403 after authentication is reported as `404` (Docker Hub answers 401 for missing repositories) and not cached. The client's `Accept` header is ignored: the proxy requests OCI and Docker v2 manifest and list types and serves what the upstream returns.
- **Tags and referrers:** `tags/list` and `referrers` are passed through live, not cached. A group merges the tags of its readable members, following a member's `Link: rel="next"` pages until its list is complete; a member whose list cannot be read to the end makes the answer `Cache-Control: no-store`.
- **Content types:** `/v2/...` responses are served from the application origin, so they carry the same `sandbox` Content-Security-Policy and `Content-Disposition: attachment` as `/repos/...`. A manifest or listing is sent only under a registry JSON type (`application/vnd.*+json`, `+prettyjws`, `application/json`); a manifest whose pushed or upstream type is anything else (for example `text/html`) is served as `application/octet-stream` by a hosted repository, and under the type named in its own `mediaType` or the Docker v2 manifest type by a proxy; referrers answered by an upstream under another type are sent as `application/vnd.oci.image.index.v1+json`.
- **Writes:** uploads, manifest push and deletes on a proxy or group are `405 UNSUPPORTED` (`Allow: GET, HEAD`).
- **Storage:** cached content is a normal proxy cache (see [proxy repositories](../repositories/hosted-proxy-group.md#proxy-repositories)): it is listed in the package list, deduplicated by sha256 in the blob store and removed with the repository. Blobs are fully downloaded and verified before the first byte is sent, so a large first pull starts late. Digest-addressed entries have no eviction other than an admin purge. There is no `_catalog`.

## Garbage collection

Deleting a tag, or pushing a new image to an existing tag, leaves the old manifest behind untagged, and the layers only that manifest used stay in storage. Registry garbage collection reclaims them: it deletes untagged manifests older than a grace period and then the blobs nothing references any more. [Cleanup policies](../repositories/cleanup-and-quotas.md) do not cover Docker, so this is the way to reclaim Docker storage.

**Trash.** Manifests in the [trash](../operations/trash.md) are not collected by garbage collection, and neither are the layers they reference: they stay in storage until the manifest is purged (or restored). Garbage collection only sees live manifests, so a trashed tagged image never turns into an "untagged" candidate. Once the purge worker has removed a manifest, its layers become collectable by the normal rules.

**Limitation:** blob lookups (`GET`/`HEAD /v2/<name>/blobs/<digest>`) are by repository and digest and do not look at manifests, so a layer or config blob of a trashed image can still be fetched by digest while it sits in the trash (the manifest and its tags answer `404`). Hiding it would need the same check on every blob lookup, which is also used by uploads, cross-repository mounts and manifest validation, and could break pushes of images that reuse the layer; it is therefore not done. Restoring a tag whose manifest is in the trash answers `409 manifest_in_trash`; restore the manifest instead.

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
