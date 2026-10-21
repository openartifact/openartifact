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
| `DELETE /<path>` | delete |

## Upload

- Limit 5 GiB (`413` above it); the body is spooled to a temporary file while hashing.
- Content type is the request `Content-Type`, else detected from the first 512 bytes.
- An empty path is `400`. `PUT` to an existing path replaces it (same record, new content) and the replaced blob is removed when unreferenced: raw files are always mutable.

## Presigned uploads

On hosted raw repositories backed by S3 or GCS, large files can skip the server: `POST /api/v1/repositories/{repo}/uploads` returns a presigned `PUT` URL, the client uploads straight to the bucket, and `POST .../uploads/{id}/complete` verifies size and sha256 and records the file like a normal `PUT`. The CLI wraps this as `oa artifact upload --presigned <repo> <file>`. Up to 5 GiB per file. Flow, errors, cleanup and the quota exception: [Presigned uploads](../operations/storage.md#presigned-uploads). Raw downloads can likewise be redirected, see [Presigned URLs](../operations/storage.md#presigned-urls).

## Proxy and group

A raw proxy mirrors files by path from `upstream_url`. Because raw files are mutable, every file in a raw proxy is cached only for `cache_ttl_seconds` (default 1 hour) and refetched afterwards; nothing is treated as immutable. Stale-serve, negative caching and SSRF rules are in [hosted, proxy, group](../repositories/hosted-proxy-group.md). A raw group returns the first member (hosted or proxy, in order) that has the path.
