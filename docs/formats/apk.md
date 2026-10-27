<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Alpine (apk)

Alpine Linux package repository laid out like an Alpine mirror, with a generated and signed `APKINDEX.tar.gz` per directory.

| | |
|---|---|
| Format id | `apk` |
| Repo URL | `HOST/repos/REPO/<branch>/<repo>` (index `<arch>/APKINDEX.tar.gz`, packages `<arch>/<name>-<version>.apk`) |
| Repository types | hosted |
| Minimum client | apk-tools from Alpine 3.6 (SHA-256 index signatures) |

## Client setup

`apk` appends `/<arch>/APKINDEX.tar.gz` to every line of `/etc/apk/repositories`, so the line is the repository URL up to the `<repo>` directory. The index is signed, so install the repository's public key first:

```sh
curl -fsSL -o /etc/apk/keys/openartifact-REPO.rsa.pub HOST/repos/REPO/public.key
echo "HOST/repos/REPO/v3.20/main" >> /etc/apk/repositories
apk update
apk add PACKAGE
```

In a Dockerfile:

```dockerfile
FROM alpine:3.20
RUN wget -O /etc/apk/keys/openartifact-REPO.rsa.pub HOST/repos/REPO/public.key \
 && echo "HOST/repos/REPO/v3.20/main" >> /etc/apk/repositories \
 && apk add --no-cache PACKAGE
```

- The key file name must be the key name inside the index signature (default `openartifact-REPO.rsa.pub`); the key is also served as `HOST/repos/REPO/<keyname>`. [Signing key](#signing-key) shows how to read the name.
- A private repository (not public, no anonymous read) needs credentials for the key and for the repository: put them in the URL (`https://USERNAME:<TOKEN>@HOST/repos/REPO/v3.20/main`) and download the key with `wget --user USERNAME --password <TOKEN>`.
- Alpine 3.5 and older reject SHA-256 signatures and cannot use these repositories. Verified with apk-tools 2.14 (Alpine 3.20), apk-tools 3.x on edge (which still reads the v2 index) and Alpine 3.6 to 3.16.

The web UI and the MCP server show the same snippets on the repository page.

## Layout

Served relative to `HOST/repos/REPO`; the directories are whatever you upload to:

| Path | Notes |
|---|---|
| `<branch>/<repo>/<arch>/APKINDEX.tar.gz` | generated, signed index of that directory |
| `<branch>/<repo>/<arch>/<name>-<version>.apk` | package download |
| `public.key`, `<keyname>` | public key of the repository, see [signing key](#signing-key) |

`<branch>` is for example `v3.20` or `edge`, `<repo>` for example `main` or `community`, `<arch>` for example `x86_64` or `aarch64`.

## Upload

Needs write permission on `REPO` ([RBAC](../security/rbac.md)). Packages do not need to be signed; the index is.

```sh
# curl: the path decides branch, repository and architecture
curl -T my-pkg-1.0-r0.apk -H "Authorization: Bearer <TOKEN>" \
  HOST/repos/REPO/v3.20/main/x86_64/my-pkg-1.0-r0.apk

# REST API (multipart)
curl -H "Authorization: Bearer <TOKEN>" -F file=@my-pkg-1.0-r0.apk -F branch=v3.20 -F repo=main \
  HOST/api/v1/repositories/REPO/apks

# oa CLI
oa artifact upload --branch v3.20 --apk-repo main --arch x86_64 REPO my-pkg-1.0-r0.apk
```

| Form field (`POST /api/v1/repositories/REPO/apks`) | Default | Notes |
|---|---|---|
| `file` | required | the `.apk` |
| `branch` | `edge` | first directory |
| `repo` | `main` | second directory |
| `arch` | package architecture | required for `noarch` packages only |

- `DELETE <branch>/<repo>/<arch>/<name>-<version>.apk` (`curl -X DELETE`) deletes one package; it goes to the [trash](../operations/trash.md).
- The file name must be the canonical `<name>-<version>.apk`.
- The arch directory must equal the architecture in `.PKGINFO`. A `noarch` package is stored in the arch directory you name (the path of the `PUT`, or the `arch` field / `--arch` of the REST upload, which is required for `noarch`); upload it to every architecture directory that should offer it.
- The package is validated: the gzip segments, `.PKGINFO`, the `datahash` of the data segment and the tar layout. A malformed package is `400`.
- The same name and version in the same directory is `409 CONFLICT`: packages are immutable.
- Quotas, license policy (the `license` of `.PKGINFO`), quarantine, scanning, webhooks and the [immutable releases](../operations/trash.md#immutable-releases) guard apply as for other hosted formats; downloads are counted. Every version counts as a release.
- Only the Alpine v2 package format (a concatenation of gzip segments) is supported. apk v3 (ADB) `.apk` files cannot be uploaded; apk-tools 3 still reads the v2 index and the packages Alpine publishes today.

## Index

- `APKINDEX.tar.gz` is generated per `<branch>/<repo>/<arch>` directory by a background worker that coalesces requests, shortly after an upload, delete or restore. A directory has no index until the worker has run, so `APKINDEX.tar.gz` can answer `404` for a moment after the first upload.
- Entries are verified by identity: the `C:` checksum is `Q1` plus the base64 SHA-1 of the package's control segment, and the data segment's SHA-256 is checked against `datahash` at upload.
- The index is signed with the repository's RSA key as `.SIGN.RSA256.<keyname>` (SHA-256).

## Signing key

| | |
|---|---|
| Generated | lazily on first use: RSA-4096, default name `openartifact-REPO.rsa.pub` (shortened with a digest suffix if the name would exceed 87 bytes) |
| Stored | private key sealed with `OPENARTIFACT_SECRET_KEY` (AES-GCM) in the database; never returned |
| Public key | `GET HOST/repos/REPO/public.key` or `HOST/repos/REPO/<keyname>`; authentication is needed unless the repository is public or anonymous read is on |

Admin API (global manage permission; [REST reference](../reference/api/repositories.md)):

```sh
# key name, fingerprint, public key
curl -H "Authorization: Bearer <TOKEN>" HOST/api/v1/repositories/REPO/apk-key

# rotate: generate a new key
curl -X POST -H "Authorization: Bearer <TOKEN>" HOST/api/v1/repositories/REPO/apk-key

# import an RSA key written by abuild-keygen
curl -X POST -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d "{\"private_key\": $(jq -Rs . < mykey.rsa), \"key_name\": \"mykey.rsa.pub\"}" \
  HOST/api/v1/repositories/REPO/apk-key
```

- Response: `{key_name, fingerprint, public_key, created_at, updated_at}`.
- Import takes a PKCS#8 or PKCS#1 RSA private key of 2048 to 16384 bits. `key_name` is optional, must match `^[A-Za-z0-9._@+-]+\.rsa\.pub$` and be at most 87 bytes.
- Rotating or importing re-signs every index of the repository, and **clients must install the new public key** (`apk update` fails with `UNTRUSTED signature` until they do). Rotation is audited as `repository.updated`.
- The repository **Settings** page has an apk key panel for the same operations.

### Without OPENARTIFACT_SECRET_KEY

The private key cannot be sealed, so no key is created: the index is served unsigned, `public.key` answers `404` and the admin API answers `409 secret_key_required`. Clients then need `apk --allow-untrusted`, which is not recommended. Set the secret and the next index regeneration signs.

## Backup and rebuild

The signing key is not part of [backups](../operations/backup.md) or declarative export, and it cannot be derived from stored packages. After a restore or a [rebuild from storage](../operations/rebuild-from-storage.md) a new key is generated on first use, and every client must install the new public key. To keep the same key across rebuilds, import it again with the admin API afterwards.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `UNTRUSTED signature` | the public key is not in `/etc/apk/keys`, or its file name differs from the key name in the index: install `public.key` under the name in `apk-key`'s `key_name`; reinstall it after a rotation, import or rebuild |
| `BAD signature` | the key file does not match the signer (an old key after rotation), or a proxy or cache altered the index: reinstall the current public key and clear intermediate caches |
| `APKINDEX.tar.gz` is `404` right after the first upload | the index is still being generated; retry in a moment |
| `404` for `public.key` | the repository has no key because `OPENARTIFACT_SECRET_KEY` is not set |
| `401`/`403` from `apk update` | private repository: credentials in the URL, token with read permission |
| upload `400` | not a valid v2 `.apk`, arch directory differs from the package architecture, or the file name is not `<name>-<version>.apk` |
| upload `409` | that name and version already exist in the directory |

Hosted only; proxy and group apk repositories are planned.
