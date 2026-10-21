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
- **Delete:** a tag reference removes only that tag. A digest reference removes the manifest and every tag of that image that points to it and frees the blob when nothing else references it. Blob delete removes the repository's blob record and frees storage when unreferenced. All return `202`.
- **Quarantine:** manifest and blob reads of quarantined content are refused.
- **Quota:** storage quotas are not enforced for Docker pushes.

## Signing

The registry does not verify image signatures; signature artifacts are stored like any other manifest and are listed through `referrers` when they carry a `subject`. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).
