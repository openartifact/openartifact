<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Docker / OCI

Push and pull container images and OCI artifacts with `docker`, `podman` and other OCI clients. OpenArtifact serves the OCI Distribution API at the host root (`/v2/`). Use a hosted repository for your own images, a proxy to cache an upstream registry, and a group to put several of them behind one name.

| Item | Value |
|---|---|
| Registry URL | `HOST/v2/` |
| Image reference | `HOSTPORT/REPO[/image]:tag` |
| Format id | `docker` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://registry-1.docker.io` (Docker Hub) |

`HOSTPORT` is the host and port without scheme, for example `localhost:8080`. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) for the repository types.

## Client setup

The web UI connect panel of a repository shows the same commands with your own URL.

```sh
docker login HOSTPORT -u <USERNAME> -p <TOKEN>
docker tag my-image:latest HOSTPORT/REPO:latest
docker push HOSTPORT/REPO:latest
docker pull HOSTPORT/REPO:latest
```

- The username is ignored. The token is the password.
- The registry offers only a Basic challenge (there is no token service), so docker sends the credentials directly.
- Docker treats plain-HTTP registries other than `localhost` as insecure. Serve OpenArtifact over TLS or list it under `insecure-registries` in the daemon configuration.
- Image names must be lowercase, so use lowercase names for Docker repositories.

## Publish

Push with `docker push` as shown above. Pushing to a proxy or group is `405 UNSUPPORTED` (`Allow: GET, HEAD`).

The first path segment after `/v2/` is the OpenArtifact repository. The rest is the image inside it.

| Reference | Repository | Image |
|---|---|---|
| `HOSTPORT/REPO:latest` | `REPO` | default (empty) image |
| `HOSTPORT/REPO/app:1` | `REPO` | `app` |
| `HOSTPORT/REPO/org/team/app:1` | `REPO` | `org/team/app` |

- Image names follow the distribution-spec grammar: lowercase alphanumerics with `.`, `_`, `__` or `-` separators and `/` between components. Otherwise the answer is `400 NAME_INVALID`. An unknown repository is `404 NAME_UNKNOWN`.
- A name may contain a segment called `manifests`, `blobs` or `tags`. The path is parsed from the right.

### Image scoping and permissions

- Tags and manifests belong to one image.
- Blobs are stored once per repository, but an image only sees the blobs it was pushed, mounted or referenced by one of its manifests.
- A `GET`, `HEAD` or `DELETE` of another image's digest is `404 BLOB_UNKNOWN`. A manifest that references such a blob is `400 MANIFEST_BLOB_UNKNOWN`.
- To reuse a layer of another image, push the same bytes again (the server stores them once) or mount it with `from=<repo>/<other image>`. A mount needs read permission on the source image.
- A blob that no manifest references yet stays visible to every image of its repository until an image claims it.
- Permissions are checked against the repository. The resource path starts with the image (`org/team/app/manifests/1`), so an [RBAC](../security/rbac.md) `path_pattern` can limit a permission to one image, blobs included.

### Push behaviour

- **Blob upload.** An upload session is one growing object in the blob store (under `uploads/`, never listed as a blob). Each `PATCH` appends its bytes to it and records the new offset, the running SHA-256 state and the storage position in the database in one step. Any replica can take the next request of a session, so a push works behind a round-robin load balancer without a shared volume. The `PUT` appends its own body, then verifies the digest from the recorded state (the data already stored is not read again) and makes the object the blob: a server-side copy on S3-compatible storage, a block commit on Azure, a hard link on the filesystem. Sessions idle longer than `workers.dockerUploadTtl` (default `24h`) are removed with their data, including the unfinished multipart upload on S3. Completing an upload for a blob the repository already has succeeds with `201`. The stored object is always the one whose digest was verified. See the next item for several replicas.
- **Several replicas.** The upload object lives in the blob store, which every replica shares, except on the `oci` storage backend, which cannot hold it. With `oci` the upload is kept in `workers.dockerUploadDir` (default `/tmp/openartifact/docker-uploads`) on the replica that took the `PATCH`: run one replica, or mount that directory on a volume all replicas share; the `PUT` then streams the verified upload into the backend. A request holds a short lease on the session row (renewed while it runs), so requests of one session never overlap: one that arrives while another is running gets `409 BLOB_UPLOAD_INVALID`, or, on the same replica, waits for it. A `PATCH` whose offset is no longer the session's, because another request moved it first, also gets `409`.
- **Cost.** A chunked push moves each layer over the network once: the client's `PATCH` requests carry it to the blob store, and finishing the blob is a server-side copy or rename. The server holds a few part buffers (up to 64 MiB) in memory, not a layer, and no local disk, except with `oci`.
- **Manifest push.** Manifests are stored by content. The answer is `201` with `Location` and `Docker-Content-Digest`. Pushing by tag moves the tag if it exists. The default media type is `application/vnd.docker.distribution.manifest.v2+json`. An OCI `subject` is recorded and echoed in `Oci-Subject`.
- **Mount.** A mount succeeds when the blob is already available to the target image, or when the caller may read the source repository and the blob is not quarantined. Otherwise the registry opens a normal upload session.
- **Limits.** The manifest body is limited to 4 MiB (`413 SIZE_INVALID`). Every referenced config and layer blob must be available to the image.

| Situation during upload or push | Response |
|---|---|
| `Content-Range` does not start at the current offset | `416 RANGE_INVALID` |
| Another `PATCH` of the same session was accepted first (two clients, or a retry racing the original) | `409 BLOB_UPLOAD_INVALID`; the losing chunk is dropped |
| The session's data is gone from the blob store, or the session was opened by a release that stored chunks as separate objects | `404 BLOB_UPLOAD_UNKNOWN`; start the push again |
| Digest of the upload does not match | `400 DIGEST_INVALID`; the upload is discarded |
| `PATCH` while a `PUT` completes the same session on the same replica | waits, then `404 BLOB_UPLOAD_UNKNOWN` |
| `PATCH` while a `PUT` completes the same session on another replica | the `PATCH` is answered `409 BLOB_UPLOAD_INVALID` while the `PUT` holds the session, and `404 BLOB_UPLOAD_UNKNOWN` after it; its bytes are never part of the blob the `PUT` verified |
| Manifest pushed by digest does not match its body's SHA-256 | `400 MANIFEST_INVALID` |
| Push over the repository quota | `507` with the OCI `DENIED` error |

Pushes are checked against the repository's [storage quota](../repositories/cleanup-and-quotas.md#what-counts): blob bytes (layers and configs, once per repository) and one artifact per manifest. Manifest delete, blob delete and garbage collection give the usage back.

<a id="endpoints"></a>
## Install and consume

Pull with `docker pull HOSTPORT/REPO/image:tag`. The endpoints are:

| Request | Behaviour |
|---|---|
| `GET /v2/` | `200` with `Docker-Distribution-Api-Version: registry/2.0` |
| `GET /v2/_catalog` | images the caller may read, paginated with `n` and `last` |
| `GET`, `HEAD`, `PUT`, `DELETE /v2/<name>/manifests/<ref>` | manifests by tag or digest |
| `GET`, `HEAD`, `DELETE /v2/<name>/blobs/<digest>` | blobs; a `GET` with `Range: bytes=<from>-<to>` answers `206` with `Content-Range` (`416` past the end), so an interrupted pull resumes |
| `POST /v2/<name>/blobs/uploads/` | start an upload; `?mount=<digest>&from=<name>` attempts a cross-repository mount |
| `GET`, `PATCH`, `PUT`, `DELETE /v2/<name>/blobs/uploads/<uuid>` | status, chunk, complete (`?digest=`), cancel |
| `GET /v2/<name>/tags/list` | tags of the image |
| `GET /v2/<name>/referrers/<digest>` | OCI index of manifests whose `subject` is the digest, with `artifactType` and `annotations` |

Errors use the registry JSON shape `{"errors":[{"code","message","detail"}]}`. An unsupported method is `405 UNSUPPORTED` with `Allow`.

### Tag list

- `n` is the page size (default 100, at most 1000). `last` is the last tag of the previous page.
- A `Link: <...>; rel="next"` header is sent when more tags exist.
- `n=0` returns an empty list. An invalid `n` is `400 PAGINATION_NUMBER_INVALID`.
- An image without any manifest is `404 NAME_UNKNOWN`.

### Catalog

`GET /v2/_catalog` returns `{"repositories":["<repository>/<image>", ...]}`, in bytewise order. A repository's default image is listed as just `<repository>`.

- Pagination is the same as for the tag list: `n`, `last` and a `Link: </v2/_catalog?last=...&n=...>; rel="next"` header while more names exist.
- Any authenticated caller may ask. The list holds only images the caller may read (read access to `<image>/tags/list`) and is empty when the caller reads none. The catalog never reveals a name the caller could not pull.
- Anonymous callers get the same answer as for `GET /v2/`: `401` unless anonymous reads are enabled.
- A hosted repository lists images with a live manifest. Images whose manifests are all in the trash disappear.
- A proxy lists the images it has cached a manifest of.
- A group lists the images of its members under the group's name, when the caller may read the group and a member that holds the image. A repository's default image is not listed through a group.
- Blocked and deleted repositories are not listed.
- If the server lacks the catalog interfaces, the answer is `501 UNSUPPORTED`.

### Referrers

Signature, SBOM and attestation manifests that carry a `subject` are listed through `referrers`. Proxies pass `referrers` through live.

<a id="proxy-and-group-repositories"></a>
## Proxy

A docker proxy pulls through from an upstream registry. It is served under `/v2/` only, not under `/repos/...`. The proxy is read-only.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"dockerhub","format":"docker","repo_type":"proxy"}'
docker pull HOSTPORT/dockerhub/library/alpine:3.20
docker pull HOSTPORT/dockerhub/alpine:3.20            # same image on Docker Hub
```

`upstream_url` defaults to Docker Hub (`https://registry-1.docker.io`). Only its scheme and host are used. On Docker Hub, a single-segment image such as `alpine` is mapped to `library/alpine`.

### Upstream authentication

- The proxy answers the upstream's `WWW-Authenticate` challenge itself, for Bearer and Basic.
- For a Bearer challenge, the proxy asks the token service for a token with the challenged `scope`. It sends `upstream_username` and `upstream_password` as Basic credentials, or none for an anonymous token.
- Stored credentials go only to a token realm on the upstream's own origin, or to an `https` realm on the same registrable domain (Docker Hub's `auth.docker.io` for `registry-1.docker.io`). Any other realm is asked anonymously and a warning is logged.
- The token realm passes the same upstream-address checks as `upstream_url`.
- Tokens are cached in memory per image until shortly before they expire. Changing the password never reuses a token minted with the old one.

### Verification and caching

- **Verification.** Every manifest and blob is downloaded and its sha256 or sha512 is checked against the requested digest and the upstream's `Docker-Content-Digest` before it is cached or served. A mismatch is `502 UNAVAILABLE` and nothing is cached. Blobs are fully downloaded and verified before the first byte is sent, so a large first pull starts late.
- **Digest-addressed content.** Manifests and blobs addressed by digest never change, so they are never revalidated. One that has not been pulled for `workers.proxyDockerDigestTtl` (default `720h`, env `OPENARTIFACT_PROXY_DOCKER_DIGEST_TTL`) is evicted by the proxy cache worker together with its blob. A blob that a tag entry or another entry still references stays. The next pull fetches evicted content from the upstream again. A pull in the second half of the period renews the entry. `0` disables the eviction: entries then stay until an admin purge or the repository's removal.
- **Tags.** A tag is served from the cache for `cache_ttl_seconds`. Afterwards it is revalidated with a `HEAD` and refetched only when the digest changed. A tag can outlive the digest entry it points to. The next pull of that digest refetches it.
- **Upstream failure.** When the upstream is unreachable, rate-limited (429) or answers 5xx, an expired tag is served stale for up to `max_stale_seconds` (`Warning: 110`), then `502`. An upstream 404 is cached for `negative_cache_ttl_seconds`. An upstream 401 or 403 after authentication is reported as `404` and not cached, because Docker Hub answers 401 for missing repositories.
- **Accept header.** The client's `Accept` header is ignored. The proxy requests OCI and Docker v2 manifest and list types and serves what the upstream returns.
- **Storage.** Cached content is a normal proxy cache (see [proxy repositories](../repositories/hosted-proxy-group.md#proxy-repositories)). It is listed in the package list, deduplicated by sha256 in the blob store and removed with the repository.
- **Tags and referrers.** `tags/list` and `referrers` are passed through live and are not cached.

### Minimum age and curation

- **Minimum age.** `min_age_hours` has no effect on docker proxies: the upstream registry dates no tag.
- **Curation.** Rules gate manifests only. See [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation) for the general rules.
- Rules name an image as written below the repository (`library/alpine`) and a version as the tag or digest the client requests (`3.20`, `sha256:...`). Block a tag and its digest separately.
- A blocked manifest `GET` or `HEAD` answers `403 DENIED` with the reason, even when cached.
- Blobs addressed by digest are not gated, but a client cannot find them without the manifest. Tag lists are passed through.

## Group

A docker group serves manifests and blobs from the first readable member that has them. It is read-only.

- `tags/list` is merged across the readable members: sorted, deduplicated and paginated with `n` and `last`. The group follows a member's `Link: rel="next"` pages until its list is complete. A member whose list cannot be read to the end makes the answer `Cache-Control: no-store`.
- `HEAD` is answered like `GET` without the body.
- Hosted members win a package name: when a readable hosted member holds a repository path, `tags/list`, manifests and blobs of that path come only from hosted members. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).

## Limits and behaviour

- **Delete.** Deleting a tag removes only that tag. The manifest and its blobs stay until [garbage collection](#garbage-collection) removes them. Deleting a manifest by digest removes the manifest and every tag of that image that points to it, and frees the blob when nothing else references it. Deleting a blob removes the repository's blob record and frees storage when unreferenced. All answer `202`.
- **Trash.** With the default [trash](../operations/trash.md) retention, a tag or manifest delete moves the item to the trash instead. A manifest takes its tags with it, and its blobs stay referenced until purge. The client sees the same `202` and a following pull is `404`. Restoring a tag whose manifest is in the trash answers `409 manifest_in_trash`: restore the manifest instead.
- **Immutable releases.** In a repository with `immutable_releases` every tag is a release. Moving a tag to a different digest, deleting a tag and deleting a manifest answer `409` unless an admin sends `?force=true`. Re-pushing the same digest is not a change. See [immutable releases](../operations/trash.md#immutable-releases).
- **Quarantine.** Manifest and blob reads of quarantined content are refused.
- **Content types.** `/v2/...` responses carry the same `sandbox` Content-Security-Policy and `Content-Disposition: attachment` as `/repos/...`. A manifest or listing is sent only under a registry JSON type (`application/vnd.*+json`, `+prettyjws`, `application/json`). A manifest with any other type (for example `text/html`) is sent as `application/octet-stream` by a hosted repository. A proxy sends it under the type named in its own `mediaType`, or the Docker v2 manifest type. Referrers an upstream answers under another type are sent as `application/vnd.oci.image.index.v1+json`.
- **Blobs of trashed images.** A layer or config blob that only trashed manifests of the repository reference answers `404 BLOB_UNKNOWN` to `GET` and `HEAD /v2/<name>/blobs/<digest>` while it sits in the trash. It is not offered as a mount source. Restoring a manifest that references it makes it fetchable again. A blob that a live manifest also references keeps being served, and so does a blob no manifest references yet (an upload awaiting its manifest). Pushing an image that reuses a trashed layer works: the client uploads the layer again and it is served once the new manifest exists.

## Garbage collection

Deleting a tag, or pushing a new image to an existing tag, leaves the old manifest behind untagged, and the layers only that manifest used stay in storage. Registry garbage collection reclaims them: it deletes untagged manifests older than a grace period and then the blobs nothing references any more. A [cleanup policy](../repositories/cleanup-and-quotas.md) removes old tags and runs this collection afterwards when it removed tags permanently. The admin API below runs it on demand.

**Trash.** Manifests in the [trash](../operations/trash.md) are not collected, and neither are the layers they reference. They stay in storage until the manifest is purged or restored. Once the purge worker has removed a manifest, its layers become collectable by the normal rules.

**What is kept.** A manifest is deleted only when it is older than the grace period (its last push or re-push) and none of these holds:

- a tag of the same image points at it;
- it is a child of a kept index or manifest list (a multi-arch image keeps its per-platform manifests);
- it is the `subject` of a kept manifest, or a referrer (signature, SBOM, attestation) of a kept subject: the pair lives and dies together;
- it is tied to a manifest in one of the previous ways, directly or through several steps.

After the manifests, a blob record of the repository is deleted when no remaining manifest of the repository references it **and** no client has used it within the grace period. A blob counts as used when it is uploaded, re-pushed, mounted or probed with `HEAD`. The storage object is removed only when no other repository or format still references the same content. Layers uploaded but never referenced by a manifest, for example by an aborted push, are collected the same way once they are older than the grace period.

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

Both body fields are optional. An empty body is a real run with the configured grace period.

| Request field | Meaning |
|---|---|
| `dry_run` | report what would be deleted and delete nothing |
| `grace_period_seconds` | `0` means no grace; negative values and values above `3153600000` are `400` |

The response reports:

| Response field | Meaning |
|---|---|
| `repository` | the repository name |
| `dry_run` | whether this was a preview |
| `grace_period_seconds` | the grace period used |
| `manifests_deleted` | manifests removed |
| `blob_refs_deleted` | blob records of this repository removed |
| `blobs_freed`, `bytes_freed` | storage objects actually removed; in a dry run an upper bound that ignores sharing with other repositories |
| `failures` | storage deletions that failed and were logged; a later cleanup pass retries them |
| `manifests_missing_body` | manifests whose body is missing from storage. Their layers are unknown, so while any exists no blob of the repository is removed; the server logs each one with its digest. Delete those manifests to let garbage collection free blobs again |
| `manifests`, `blobs` | the entries involved, capped at 1000 each, with `truncated` set when capped |

`409` means a run is already in progress for the repository. `400` means the repository is not a Docker repository.

**Safety.**

- Runs on one repository never overlap, also across replicas.
- Every deletion re-checks its conditions (still old enough, still untagged, still not tied to a kept manifest) at the moment it deletes. A push is either fully visible to the collector or happens after it.
- The grace period protects a push in progress: layers are uploaded before the manifest that references them, and a tag is set after the manifest is stored. Keep it longer than your slowest push.
- With `grace_period_seconds: 0`, a push running at that moment can lose layers or its manifest. Use it on a quiet repository.

**Consequences.** A reference by digest to an untagged manifest (`image@sha256:...`) stops resolving once the grace period has passed. Pin by tag, or keep a tag on anything that must stay pullable.

## Boot from an image

Images built for bootc or `rpm-ostree` are ordinary OCI images with `ostree.*` annotations. The registry needs no special setup for them: store them in a hosted repository, or proxy a registry that holds them. For a private repository, give the client the same token as for `docker login` (Basic, the token as password). Use HTTPS in production.

## Signing

The registry does not verify image signatures. Signature artifacts (for example cosign signatures stored as tags) are stored like any other manifest and are listed through `referrers` when they carry a `subject`. The client verifies them.

OpenArtifact can also attach the signed SBOM and VEX of an image itself, as referrers: see [Attaching to an OCI image](../security/sbom.md#attach-to-an-image-hosted-docker-repositories). GPG signing covers only Debian and RPM ([signing](../security/signing.md)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `docker login` or push fails with an HTTP/TLS error | plain-HTTP registry other than `localhost` | serve over TLS or add it to `insecure-registries` |
| `400 NAME_INVALID` | uppercase or invalid characters in the name | use lowercase names that follow the grammar above |
| `404 NAME_UNKNOWN` | the first path segment is not a docker repository | create the repository or fix the name |
| `400 MANIFEST_BLOB_UNKNOWN` on push | a referenced layer is not visible to this image | push the layer again, or mount it from an image you may read |
| `404 BLOB_UNKNOWN` for a layer another image has | blobs are scoped per image | push or mount the blob into this image |
| `405 UNSUPPORTED` on push | target is a proxy or group | push to a hosted repository |
| `403 DENIED` on a proxy pull | a curation rule blocks the tag or digest | check the rules; block and allow tag and digest separately |
| `502 UNAVAILABLE` on a proxy pull | digest mismatch, or the upstream failed | retry; check the upstream and `upstream_url` |
| `507` on push | repository quota exceeded | free space or raise the quota |
| `409` on tag move or delete | `immutable_releases` is on | use a new tag, or an admin sends `?force=true` |
| old untagged `image@sha256:...` no longer pulls | garbage collection removed it after the grace period | keep a tag on images that must stay pullable |
