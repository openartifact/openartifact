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
| Repository types | hosted, [proxy](#proxy-repositories), [group](#group-repositories) |
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
- A private repository (not public, no anonymous read) needs credentials for the key and for the repository: put them in the URL (`https://USERNAME:<TOKEN>@HOST/repos/REPO/v3.20/main`) and download the key with `wget --header "Authorization: Bearer <TOKEN>"` (BusyBox `wget`, the one Alpine ships, has no `--user`/`--password`).
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
- The arch directory must equal the architecture in `.PKGINFO`. A `noarch` package is stored in the arch directory you name (the path of the `PUT`, or the `arch` field / `--arch` of the REST upload, which is required for `noarch`); upload it to every architecture directory that should offer it. As in Alpine's own mirrors, the directory's index lists it with the architecture of the directory (`apk index --rewrite-arch`), so `apk` downloads it from `<arch>/` and not from a `noarch/` directory; the `.apk` file itself stays `noarch`.
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

## Proxy repositories

An apk proxy is a pull-through cache of an Alpine mirror. Set `upstream_url` to the mirror root, for example `https://dl-cdn.alpinelinux.org/alpine` (there is no default; the web form suggests this one), and create it with `repo_type: proxy`:

[Package curation](../repositories/hosted-proxy-group.md#package-curation) refuses the download of a blocked package file (`403` with the reason, even when cached): rules name the package and a version as `<version>-r<N>` from the file name `<name>-<version>-r<N>.apk`. `APKINDEX.tar.gz` is served as the upstream has it, so apk still lists a blocked package.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"alpine-mirror","format":"apk","repo_type":"proxy","upstream_url":"https://dl-cdn.alpinelinux.org/alpine"}'
```

The mirror tree is mirrored by path, so the repositories lines are the mirror's with `HOST/repos/REPO` in place of the mirror root:

```sh
echo "HOST/repos/alpine-mirror/v3.20/main" >> /etc/apk/repositories
echo "HOST/repos/alpine-mirror/v3.20/community" >> /etc/apk/repositories
apk update
apk add PACKAGE
```

- **No key to install.** `public.key` answers `404`: the proxy has no key of its own. Official Alpine images already trust the keys of the `alpine-keys` package (`/etc/apk/keys`), which is what signed the upstream index.
- **Index.** `<branch>/<repo>/<arch>/APKINDEX.tar.gz` is passed through byte for byte, signature included, and cached for `cache_ttl_seconds` (default 1 hour) before it is fetched again. A client therefore sees exactly the upstream index; a change upstream shows up after the TTL.
- **Packages.** A `.apk` is downloaded on first request, checked, and then cached for good (a file name is not reused by an Alpine mirror). The check: the package is parsed (its data hash is checked against the control segment) and its checksum must equal the `C:` entry that the cached upstream index of the same directory lists for that file name. A package the index does not list answers `404` (also right after a new upstream build, until the index is refetched after the TTL), a package that fails the check or is not a valid `.apk` answers `502` and is not cached.
- **Other paths** (anything but an index or a package in a `<branch>/<repo>/<arch>/` directory) answer `404` without contacting the upstream.
- **Trust model.** The proxy does not verify the upstream index signature: it has no Alpine trust keys configured. Clients verify it with the keys they already have; a client without them (a bare image, or a repository signed by another key) gets `UNTRUSTED signature` as it would from the mirror itself. A package is bound to the index by its checksum, so a tampered package fails at the proxy, but a tampered index is only caught by the client.
- **Offline behaviour, negative caching, size limits, `max_stale_seconds` and credentials for a private mirror** work as for every proxy: see [hosted, proxy, group](../repositories/hosted-proxy-group.md). With the upstream down an expired index or package is served stale within `max_stale_seconds`; a package never fetched cannot be served.
- Proxy repositories are read-only: uploads answer `405`. Quarantine applies to cached packages like to any proxy.

## Group repositories

An apk group serves several hosted and proxy apk repositories as one, at `HOST/repos/GROUP/<branch>/<repo>`. Clients read an index per directory, so the group cannot forward a request to one member: it publishes an `APKINDEX.tar.gz` of its own for every `<branch>/<repo>/<arch>` directory, merged from the members and signed with the group's own key. Create it with `repo_type: group` and `members` in priority order, as in [hosted, proxy, group](../repositories/hosted-proxy-group.md#creating-repositories).

```sh
wget -O /etc/apk/keys/openartifact-GROUP.rsa.pub HOST/repos/GROUP/public.key
echo "HOST/repos/GROUP/v3.20/main" >> /etc/apk/repositories
apk update
apk add PACKAGE
```

The key to install is the group's, not its members': `HOST/repos/GROUP/public.key` (also `/<keyname>.rsa.pub`). It is generated on first use like a hosted repository's, shown, rotated and imported with the same admin API and web panel ([signing key](#signing-key)), and needs `OPENARTIFACT_SECRET_KEY` as well: without it the group's indexes are unsigned and `public.key` answers `404`. After a rotation or import the group's indexes are signed again in the background; clients must install the new public key. The key is not part of backups.

- **Merge rule.** Per directory, the entries of the members are taken in member order.
  1. Unless `merge_proxy_versions` is `true`, an entry of a proxy member is dropped when any readable hosted member of the group holds a package of that name in any directory ([hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name)).
  2. Of the entries with the same file name (`<name>-<version>.apk`) the earliest member's is kept, so the checksum the index lists is the winner's.
  3. Everything else is kept: the other versions of a name from other members are all listed, and `apk` picks by version. An entry is copied as the member lists it, in the directory it is listed in (a `noarch` package is in the directories it was uploaded to or the upstream lists it in; nothing is copied across directories).
- **Hosted members** contribute what is in their database for the directory, proxy members what their upstream index for the directory lists (read through the proxy cache). The index signature of an upstream is not checked by the group: the group's key vouches for its own index, and a package taken from a proxy is checked against the upstream index by the proxy when it is cached.
- **Directories.** A group publishes every directory a hosted member has a package in, and the directories that were requested and that some member has (for a proxy member, its upstream's). A proxy cannot be listed, so the first request of `v3.21/main/x86_64/APKINDEX.tar.gz` for a directory only a proxy has waits for the generation; a directory no member has is `404`. A directory every member lost its packages from keeps serving an empty, signed index.
- **Downloads** come from the member whose entry the index lists (`HOST/repos/GROUP/<branch>/<repo>/<arch>/<name>-<version>.apk`), so the file is always the one whose checksum the index carries. A package the index does not list is `404`, even if a member has it. Other paths are `404`.
- **Access control.** A caller is served an index that lists only the members they can read, and downloads are authorized against the owning member, as in [Deb and RPM groups](../repositories/hosted-proxy-group.md#deb-and-rpm-groups): callers who read every member share one index, the others get one generated and signed for exactly their set of members on their first request (with the group's key), and a caller who can read no member gets `404`, `public.key` included.
- **Regeneration.** The indexes are regenerated in the background when a hosted member changes (upload, delete, trash, restore, cleanup), when the group's members or settings change, when a member is deleted or blocked, and at startup for groups that have no index yet. A proxy member has no change signal: a request that finds the indexes older than the shortest `cache_ttl_seconds` among the proxies (at least one minute) serves them and refreshes them in the background. A proxy that cannot be reached (or whose index is over 128 MiB) is left out of that run, the run is marked partial and retried after a minute; a directory that then only the unreachable proxy had keeps the index it had. A proxy that does not have a directory is a complete answer.
- Group repositories are read-only: uploads answer `405`.
