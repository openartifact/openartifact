<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Raw files

Use a raw repository as a generic file store addressed by path, for any file type. A hosted repository stores what you upload, a proxy mirrors files by path from an upstream, and a group serves the first member that has a path.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/raw/REPO/<path>` |
| Format id | `raw` |
| Repository types | hosted, proxy, group; see [hosted, proxy, group](../repositories/hosted-proxy-group.md) |
| Default upstream (proxy) | none; `upstream_url` is required |

## Client setup

The web UI connect panel shows the same commands with your own URL.

```sh
curl -fO HOST/repos/raw/REPO/path/to/file.bin
curl -u <USERNAME>:<TOKEN> -T file.bin HOST/repos/raw/REPO/path/to/file.bin
```

Use Basic authentication with the token as password, or `Authorization: Bearer <TOKEN>`. See [authentication](../security/authentication.md).

## Publish

`PUT` the file to its path. The answer is `201` with JSON.

```sh
curl -fsS -u <USERNAME>:<TOKEN> -T file.bin HOST/repos/raw/REPO/path/to/file.bin
```

- The limit is 5 GiB; larger bodies are `413`. The server hashes the body into a temporary file while it receives it.
- An empty path is `400`.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced; over quota is `507`. Replacing a path releases the old content.
- The content type is the request `Content-Type`, else detected from the first 512 bytes.
- `PUT` to an existing path replaces it: same record, new content. The replaced blob is removed once nothing references it. Raw files are mutable by default.
- In a repository with `immutable_releases`, every raw file is a release. Replacing or deleting one is `409` unless an admin passes `?force=true`. See [immutable releases](../operations/trash.md#immutable-releases).

### Presigned uploads

Hosted raw repositories can take large files without sending the bytes through the server API. Call `POST /api/v1/repositories/{repo}/uploads` to get a presigned `PUT` URL, upload to it, then call `POST .../uploads/{id}/complete`. Only the creator or an admin can complete, and completing is idempotent. The server reads the object back, verifies size and SHA-256 and records the file like a normal `PUT`.

The CLI wraps this:

```sh
oa artifact upload --presigned REPO file.bin
```

A file up to 5 GiB uploads in one request. Larger files, up to 5 TiB, use multipart upload, and the CLI switches by itself above 5 GiB.

This needs a storage backend that can stage uploads. On any other backend the server answers `501` with code `presign_unsupported`. See [presigned uploads](../operations/storage.md#presigned-uploads) for the backends, flow, errors, cleanup and the quota exception. Raw downloads can be redirected too; see [presigned URLs](../operations/storage.md#presigned-urls).

## Install and consume

Paths are relative to `HOST/repos/raw/REPO`.

| Method and path | Result |
|---|---|
| `PUT /<path>` | upload, `201` with JSON |
| `GET /<path>` | the file, with `Content-Type`, `Content-Length` and `X-Checksum-Sha256` |
| `HEAD /<path>` | the same headers only |
| `GET /` or `GET /<prefix>/` | JSON list of files under the prefix: `path`, `size`, `content_type`, `sha256`, `created_at`, `updated_at` |
| `DELETE /<path>` | delete; the file goes to the [trash](../operations/trash.md), and `GET` is `404` afterwards |

A repository with retention 0 skips the trash on delete.

Hosted raw `GET` sends no `ETag` and never answers `304`. Every conditional request gets `200` with the body. `Range` requests are not honoured: the whole file is returned with `200` and no `Accept-Ranges`.

## Proxy

A raw proxy mirrors files by path from `upstream_url`. Create one with `"format":"raw","repo_type":"proxy"` and an `upstream_url`.

- Raw files are mutable, so nothing is treated as immutable. Every file is cached for `cache_ttl_seconds` (default 1 hour) and refetched afterwards.
- A raw proxy answers `If-None-Match` with `304`.
- Stale-serve, negative caching and the rules for upstream addresses are in [hosted, proxy, group](../repositories/hosted-proxy-group.md).

### Minimum age and curation

Neither applies to a raw proxy. The API accepts `curation_mode` and `min_age_hours`, but they have no effect, because a raw upstream dates no version and has no package names to match. See [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation) for the formats that support them.

## Group

A raw group returns the first member, hosted or proxy in member order, that has the path. Versions are not merged, and `merge_proxy_versions` changes nothing for raw. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name) for the setting on other formats.

Group URLs are the same as hosted ones: `HOST/repos/raw/GROUP/<path>`.

## Limits and behaviour

- **Upload size.** 5 GiB per `PUT`; see [Publish](#publish).
- **Quota.** Over quota is `507`. Delete releases the file.
- **Quarantine.** A [quarantined](../security/scanning.md#quarantine-after-upload) file is refused on `GET` and `HEAD`.
- **Trash.** Delete moves the file to the [trash](../operations/trash.md). `404` follows until you restore it.
- **Browser safety.** Files are served from the application origin. Every `/repos/...` response, and every `/v2/...` registry response, carries `Content-Security-Policy: sandbox; default-src 'none'; frame-ancestors 'none'`. For registry responses, manifest and listing types are further restricted to registry JSON types; see [Docker](docker.md).
- **Inline display.** Only images (png, jpeg, gif, webp, avif, bmp), PDF, `text/plain` and `application/octet-stream` display inline. Any type a browser would render actively (`text/html`, SVG, XML, XHTML, JavaScript, JSON, or no type) is sent with `Content-Disposition: attachment`.

## Use raw as an OSTree remote

A raw repository can host a classic (non-container) [OSTree](https://ostreedev.github.io/ostree/) repository in `archive` mode. It is a plain tree of files served over HTTP. This was tested by hand with libostree against hosted, proxy and group raw repositories.

```sh
ostree init --repo=repo --mode=archive
ostree commit --repo=repo -b os/x86_64/tiny --tree=dir=rootfs --gpg-sign=KEYID
ostree summary -u --repo=repo --gpg-sign=KEYID

# upload every file of the repository (not .lock) under one prefix
cd repo
for f in $(find . -type f ! -name .lock | sed 's#^\./##'); do
  curl -sS -u <USERNAME>:<TOKEN> -T "$f" HOST/repos/raw/REPO/ostree/"$f"
done

# on the client
ostree remote add --gpg-import=key.gpg oa HOST/repos/raw/REPO/ostree
ostree pull oa os/x86_64/tiny
```

After each commit, upload `summary`, `summary.sig`, `refs/heads/...` and the new objects.

- Nested paths and `HEAD` work, and the content type is `application/octet-stream`.
- Commit signatures (`gpg-verify=true`, the default with `--gpg-import`) and summary signatures (`gpg-verify-summary=true`, or `sign-verify-summary=true` with ed25519 keys) are verified. A pull with another key fails.
- A private repository needs credentials in the remote URL: `http://<USERNAME>:<TOKEN>@HOST/repos/raw/REPO/ostree`. Without them the pull ends with `HTTP 401` on `summary.sig`.
- `ostree pull` does not need `Range` or conditional requests.
- A proxy of a public OSTree repository works: `ostree remote refs`, `ostree pull --commit-metadata-only` and `--subpath` were tried against `https://dl.flathub.org/repo`.
- A group serves the first member that has a path, so keep one OSTree repository per group.

A proxy caches every file, objects included, for `cache_ttl_seconds`. A new upstream commit becomes visible once the cached `summary` and `refs/heads/...` expire. Use a short TTL, for example 60, for a frequently updated repository.

Each path expires on its own clock. With `gpg-verify-summary=true`, a pull that lands after `summary` expired but before `summary.sig` did fails with `BAD signature` until both are refreshed, at most one TTL. Commit signatures are not affected.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `413` on upload | The file is above 5 GiB. Use a [presigned multipart upload](#presigned-uploads). |
| `507` on upload | The repository is over its quota. Free space or raise the quota. |
| `409` on `PUT` or `DELETE` | The repository has `immutable_releases`. An admin can pass `?force=true`. |
| `501` with `presign_unsupported` | The storage backend cannot stage presigned uploads. Use a plain `PUT`, or see [presigned uploads](../operations/storage.md#presigned-uploads). |
| `400` on upload | The path is empty. |
| `404` after delete | The file is in the trash. Restore it from the [trash](../operations/trash.md). |
| `HTTP 401` on `summary.sig` in `ostree pull` | The repository is private. Put credentials in the remote URL. |
| `BAD signature` on a proxied OSTree pull | `summary` and `summary.sig` expired at different times. Retry within one TTL, or lower `cache_ttl_seconds`. |
| `libostree` waits tens of seconds in a container | No session bus. Set `DBUS_SESSION_BUS_ADDRESS=disabled:` and `GIO_USE_PROXY_RESOLVER=dummy`. The server is not involved. |
