<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Raw files

Generic file store addressed by path, for any file type.

| | |
|---|---|
| Repo URL | `HOST/repos/raw/REPO/<path>` |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
curl -fO HOST/repos/raw/REPO/path/to/file.bin
curl -u <USERNAME>:<TOKEN> -T file.bin HOST/repos/raw/REPO/path/to/file.bin
```

Credentials are Basic with the token as password, or `Authorization: Bearer <TOKEN>`; see [authentication](../security/authentication.md).

## Routes

Relative to `HOST/repos/raw/REPO`:

| Method and path | Result |
|---|---|
| `PUT /<path>` | upload, `201` with JSON |
| `GET /<path>` | file, with `Content-Type`, `Content-Length` and `X-Checksum-Sha256` |
| `HEAD /<path>` | headers only |
| `GET /` or `GET /<prefix>/` | JSON list of files under the prefix: `path`, `size`, `content_type`, `sha256`, `created_at`, `updated_at` |
| `DELETE /<path>` | delete; the file goes to the [trash](../operations/trash.md) (`404` afterwards) unless the repository retention is 0 |

## Upload

- Limit 5 GiB (`413` above it); the body is spooled to a temporary file while hashing.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced (`507` over quota); replacing a path releases the old content, delete releases the file.
- Content type is the request `Content-Type`, else detected from the first 512 bytes.
- Files are served from the application origin, so every `/repos/...` response (and every `/v2/...` registry response, whose manifest and listing types are additionally restricted to registry JSON types, see [Docker](docker.md)) carries `Content-Security-Policy: sandbox; default-src 'none'; frame-ancestors 'none'`, and any type a browser would render actively (`text/html`, SVG, XML, XHTML, JavaScript, JSON, or no type) is sent with `Content-Disposition: attachment`. Only images (png, jpeg, gif, webp, avif, bmp), PDF, `text/plain` and `application/octet-stream` display inline.
- An empty path is `400`. `PUT` to an existing path replaces it (same record, new content) and the replaced blob is removed when unreferenced: raw files are mutable by default. In a repository with `immutable_releases` every raw file is a release, so replacing an existing path (and deleting one) is `409` unless an admin passes `?force=true` ([immutable releases](../operations/trash.md#immutable-releases)).

## Presigned uploads

On hosted raw repositories (every storage backend), large files can skip the API path of the server: `POST /api/v1/repositories/{repo}/uploads` returns a presigned `PUT` URL, the client uploads straight to the bucket, and `POST .../uploads/{id}/complete` (creator or admin only, idempotent) reads the object back, verifies size and sha256 and records the file like a normal `PUT`. The CLI wraps this as `oa artifact upload --presigned <repo> <file>`. Up to 5 GiB per file in one request and up to 5 TiB with multipart upload (the CLI switches by itself above 5 GiB). Flow, errors, cleanup and the quota exception: [Presigned uploads](../operations/storage.md#presigned-uploads). Raw downloads can likewise be redirected, see [Presigned URLs](../operations/storage.md#presigned-urls).

## OSTree repositories

A raw repository can host a classic (non-container) [OSTree](https://ostreedev.github.io/ostree/) repository in `archive` mode: it is a plain tree of files served over HTTP. Checked with libostree 2026.4 against hosted, proxy and group raw repositories.

```sh
ostree init --repo=repo --mode=archive
ostree commit --repo=repo -b os/x86_64/tiny --tree=dir=rootfs --gpg-sign=KEYID
ostree summary -u --repo=repo --gpg-sign=KEYID

# upload every file of the repository (not .lock) under one prefix
cd repo
for f in $(find . -type f ! -name .lock | sed 's#^\./##'); do
  curl -sS -u USERNAME:TOKEN -T "$f" HOST/repos/raw/REPO/ostree/"$f"
done

# on the client
ostree remote add --gpg-import=key.gpg oa HOST/repos/raw/REPO/ostree
ostree pull oa os/x86_64/tiny
```

- Nested paths, `HEAD` and conditional requests (`If-None-Match`, answered with `304` by proxies) work, and the content type is `application/octet-stream`. After each commit upload `summary`, `summary.sig`, `refs/heads/...` and the new objects.
- Commit signatures (`gpg-verify=true`, the default with `--gpg-import`) and summary signatures (`gpg-verify-summary=true`, or `sign-verify-summary=true` with ed25519 keys) are verified; a pull with another key fails.
- A private repository needs the credentials in the remote URL (`http://USERNAME:TOKEN@HOST/repos/raw/REPO/ostree`); without them the pull ends with `HTTP 401` on `summary.sig`.
- `Range` requests are not honoured (the whole file is returned with `200` and no `Accept-Ranges`); `ostree pull` does not need them.
- A **proxy** of a public OSTree repository works (`ostree remote refs`, `ostree pull --commit-metadata-only`, `--subpath`; tested against `https://dl.flathub.org/repo`). Every file of a raw proxy, objects included, is cached for `cache_ttl_seconds` (default 3600), so a new upstream commit becomes visible once the cached `summary` and `refs/heads/...` expire: use a short TTL (for example 60) for a frequently updated repository. Each path expires on its own clock, so with `gpg-verify-summary=true` a pull that lands after `summary` expired but before `summary.sig` did fails with `BAD signature` until both are refreshed (at most one TTL); commit signatures are not affected.
- A group serves the first member that has a path, so keep one OSTree repository per group.
- In a container without a session bus libostree can wait tens of seconds before its first request; set `DBUS_SESSION_BUS_ADDRESS=disabled:` and `GIO_USE_PROXY_RESOLVER=dummy`. The server is not involved.

## Proxy and group

A raw proxy mirrors files by path from `upstream_url`. Because raw files are mutable, every file in a raw proxy is cached only for `cache_ttl_seconds` (default 1 hour) and refetched afterwards; nothing is treated as immutable. Stale-serve, negative caching and SSRF rules are in [hosted, proxy, group](../repositories/hosted-proxy-group.md). A raw group returns the first member (hosted or proxy, in order) that has the path.
