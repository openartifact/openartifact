<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Alpine (apk)

Use an apk repository to host Alpine Linux packages, or to cache or merge Alpine mirrors, and install them with `apk`. The repository is laid out like an Alpine mirror, with a generated and signed `APKINDEX.tar.gz` per directory.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO/<branch>/<repo>` |
| Index | `<arch>/APKINDEX.tar.gz` |
| Packages | `<arch>/<name>-<version>.apk` |
| Format id | `apk` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | none; for example `https://dl-cdn.alpinelinux.org/alpine` |

`<branch>` is for example `v3.20` or `edge`, `<repo>` for example `main` or `community`, and `<arch>` for example `x86_64` or `aarch64`.

## Client setup

`apk` appends `/<arch>/APKINDEX.tar.gz` to every line of `/etc/apk/repositories`. The line is therefore the repository URL up to the `<repo>` directory. The index is signed, so install the repository's public key first:

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

- The key file name must equal the key name inside the index signature. The default is `openartifact-REPO.rsa.pub`. The server also serves the key as `HOST/repos/REPO/<keyname>`. [Signing key and rotation](#signing-key-and-rotation) shows how to read the name.
- A private repository needs credentials for the key and for the repository. Put them in the repository URL: `https://<USERNAME>:<TOKEN>@HOST/repos/REPO/v3.20/main`. Download the key with `wget --header "Authorization: Bearer <TOKEN>"`, because the BusyBox `wget` in Alpine has no `--user` or `--password`.
- Packages are signed by the repository index, not individually. SHA-256 index signatures need an apk-tools version that supports them. This is not verified against specific Alpine releases here.

The web UI connect panel shows the same commands with your own URL.

<a id="upload"></a>

## Publish

You need write permission on `REPO` ([RBAC](../security/rbac.md)). Packages do not need to be signed; the index is.

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

The web UI upload form asks for branch, repository and architecture.

| Form field (`POST /api/v1/repositories/REPO/apks`) | Default | Notes |
|---|---|---|
| `file` | required | the `.apk` |
| `branch` | `edge` | first directory |
| `repo` | `main` | second directory |
| `arch` | package architecture | required for `noarch` packages only |

Rules for an upload:

- The file name is the canonical `<name>-<version>.apk`.
- The arch directory equals the architecture in `.PKGINFO`.
- A `noarch` package is stored in the arch directory you name: the path of the `PUT`, or the `arch` field or `--arch` of the REST upload. Upload it to every architecture directory that should offer it.
- The directory's index lists a `noarch` package with the architecture of the directory, as Alpine's own mirrors do. `apk` downloads it from `<arch>/`. The `.apk` file itself stays `noarch`.
- The server validates the package: the gzip segments, `.PKGINFO`, the `datahash` of the data segment and the tar layout. A malformed package is `400`.
- The same name and version in the same directory is `409 CONFLICT`. Packages are immutable.
- Only the Alpine v2 package format (a concatenation of gzip segments) is accepted. apk v3 (ADB) `.apk` files cannot be uploaded.

Delete one package with `curl -X DELETE HOST/repos/REPO/<branch>/<repo>/<arch>/<name>-<version>.apk`. It goes to the [trash](../operations/trash.md).

Quotas, license policy (the `license` of `.PKGINFO`), quarantine, scanning, webhooks and the [immutable releases](../operations/trash.md#immutable-releases) guard apply as for other hosted formats. Downloads are counted. Every version counts as a release.

## Install and consume

Paths are relative to `HOST/repos/REPO`. The directories are whatever you uploaded to.

| Path | Notes |
|---|---|
| `<branch>/<repo>/<arch>/APKINDEX.tar.gz` | generated, signed index of that directory |
| `<branch>/<repo>/<arch>/<name>-<version>.apk` | package download |
| `public.key`, `<keyname>` | public key of the repository |

How the index works:

- A background job generates `APKINDEX.tar.gz` per `<branch>/<repo>/<arch>` directory shortly after an upload, delete or restore. A directory has no index until that job has run, so the index can answer `404` for a moment after the first upload.
- The `C:` checksum of an entry is `Q1` plus the base64 SHA-1 of the package's control segment. The server checks the SHA-256 of the data segment against `datahash` at upload.
- The index is signed with the repository's RSA key as `.SIGN.RSA256.<keyname>` (SHA-256).

<a id="signing-key"></a>

## Signing key and rotation

This section is for administrators.

| Item | Value |
|---|---|
| Generated | on first use: RSA-4096, default name `openartifact-REPO.rsa.pub` (shortened with a digest suffix if the name would exceed 87 bytes) |
| Stored | the private key is sealed with `OPENARTIFACT_SECRET_KEY` (AES-GCM) in the database and never returned |
| Public key | `GET HOST/repos/REPO/public.key` or `HOST/repos/REPO/<keyname>` |

Reading the public key needs authentication unless the repository is public or anonymous read is on.

The admin API needs the global manage permission ([REST reference](../reference/api/repositories.md)):

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

- The response is `{key_name, fingerprint, public_key, created_at, updated_at}`.
- Import takes a PKCS#8 or PKCS#1 RSA private key of 2048 to 16384 bits. `key_name` is optional, at most 87 bytes, and matches `^[A-Za-z0-9._@+-]+\.rsa\.pub$`.
- Rotating or importing re-signs every index of the repository. Clients must install the new public key. Until they do, `apk update` fails with `UNTRUSTED signature`.
- Rotation is audited as `repository.updated`.
- The repository **Settings** page has an apk key panel for the same operations.

### Without OPENARTIFACT_SECRET_KEY

The server cannot seal a private key, so it creates no key. The index is served unsigned, `public.key` answers `404` and the admin API answers `409 secret_key_required`. Clients then need `apk --allow-untrusted`, which is not recommended. Set the secret, and the next index generation signs.

<a id="backup-and-rebuild"></a>

### Backup and rebuild

The signing key is not part of [backups](../operations/backup.md) or declarative export, and it cannot be derived from stored packages. After a restore or a [rebuild from storage](../operations/rebuild-from-storage.md) the server generates a new key on first use, and every client must install it. To keep the same key, import it again with the admin API afterwards.

<a id="proxy-repositories"></a>

## Proxy

An apk proxy is a pull-through cache of an Alpine mirror. There is no default upstream. Set `upstream_url` to the mirror root; the web form suggests `https://dl-cdn.alpinelinux.org/alpine`.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"alpine-mirror","format":"apk","repo_type":"proxy","upstream_url":"https://dl-cdn.alpinelinux.org/alpine"}'
```

The proxy mirrors the tree by path. Your repositories lines are the mirror's, with `HOST/repos/REPO` in place of the mirror root:

```sh
echo "HOST/repos/alpine-mirror/v3.20/main" >> /etc/apk/repositories
echo "HOST/repos/alpine-mirror/v3.20/community" >> /etc/apk/repositories
apk update
apk add PACKAGE
```

- **No key to install.** `public.key` answers `404` because the proxy has no key of its own. Official Alpine images already trust the keys of the `alpine-keys` package in `/etc/apk/keys`. Those keys signed the upstream index.
- **Index.** `<branch>/<repo>/<arch>/APKINDEX.tar.gz` is passed through byte for byte, signature included. It is cached for `cache_ttl_seconds` (default 1 hour). A change upstream shows up after the TTL.
- **Packages.** A `.apk` is downloaded on first request, checked and cached for good, because an Alpine mirror does not reuse a file name.
- **Package check.** The server parses the package and checks its data hash against the control segment. Its checksum must equal the `C:` entry of the cached upstream index for that file name.
- **Package answers.** A package the index does not list is `404`. This includes a new upstream build until the index is fetched again. A package that fails the check, or is not a valid `.apk`, is `502` and is not cached.
- **Other paths.** Anything but an index or a package in a `<branch>/<repo>/<arch>/` directory answers `404` without contacting the upstream.
- **Trust.** The proxy does not verify the upstream index signature, because it has no Alpine trust keys. Clients verify it with the keys they have. A client without them gets `UNTRUSTED signature`, as it would from the mirror. A tampered package fails at the proxy; a tampered index is caught only by the client.
- **Offline, negative caching, size limits, `max_stale_seconds` and credentials for a private mirror** work as for every proxy; see [hosted, proxy, group](../repositories/hosted-proxy-group.md). With the upstream down, an expired index or package is served stale within `max_stale_seconds`. A package never fetched cannot be served.
- Quarantine applies to cached packages. A proxy is read-only: uploads answer `405`.

### Minimum age and curation

Rules follow [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Minimum age.** `min_age_hours` has no effect on an apk proxy: its upstream metadata does not date each version. See [Minimum age](../repositories/hosted-proxy-group.md#minimum-age).
- **Names.** Rules name the package and a version as `<version>-r<N>`, taken from the file name `<name>-<version>-r<N>.apk`.
- **Blocked package.** The download answers `403` with the reason, even when the file is cached.
- **Not gated.** `APKINDEX.tar.gz` is served as the upstream has it, so `apk` still lists a blocked package.

<a id="group-repositories"></a>

## Group

An apk group serves several hosted and proxy apk repositories as one, at `HOST/repos/GROUP/<branch>/<repo>`. Clients read an index per directory, so the group cannot forward a request to one member. It publishes its own `APKINDEX.tar.gz` for every `<branch>/<repo>/<arch>` directory, merged from the members and signed with the group's own key. Create the group with `repo_type: group` and `members` in priority order, as in [hosted, proxy, group](../repositories/hosted-proxy-group.md#creating-repositories).

```sh
wget -O /etc/apk/keys/openartifact-GROUP.rsa.pub HOST/repos/GROUP/public.key
echo "HOST/repos/GROUP/v3.20/main" >> /etc/apk/repositories
apk update
apk add PACKAGE
```

Install the group's key, not its members' keys: `HOST/repos/GROUP/public.key` (also `/<keyname>.rsa.pub`). The group generates, shows, rotates and imports its key with the same admin API and web panel as a hosted repository ([signing key](#signing-key-and-rotation)). It also needs `OPENARTIFACT_SECRET_KEY`: without it the group's indexes are unsigned and `public.key` answers `404`. After a rotation or import the server signs the group's indexes again in the background, and clients must install the new key. The key is not part of backups.

Merge rule, per directory, with the members in order:

1. Unless `merge_proxy_versions` is `true`, an entry of a proxy member is dropped when any readable hosted member holds a package of that name in any directory. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
2. Of the entries with the same file name, the earliest member's is kept. The checksum in the index is the winner's.
3. Everything else is kept. The other versions of a name from other members are all listed, and `apk` picks by version.

An entry is copied as the member lists it, in the directory it is listed in. Nothing is copied across directories.

- **Hosted members** contribute what is in their database for the directory. **Proxy members** contribute what their upstream index lists, read through the proxy cache.
- **Upstream signatures** are not checked by the group. The group's key vouches for its own index. The proxy checks a package it takes from a proxy member when it caches it.
- **Directories.** A group publishes every directory a hosted member has a package in. It also publishes directories that were requested and that some member has. A proxy cannot be listed, so the first request for a directory only a proxy has waits for the generation. A directory no member has is `404`. A directory whose members all lost their packages keeps serving an empty, signed index.
- **Downloads** come from the member whose entry the index lists, at `HOST/repos/GROUP/<branch>/<repo>/<arch>/<name>-<version>.apk`. A package the index does not list is `404`, even if a member has it. Other paths are `404`.
- **Access control.** A caller gets an index that lists only the members they can read. Downloads are authorized against the owning member. Callers who read every member share one index. Other callers get one signed with the group's key for exactly their set of members, generated on their first request. A caller who can read no member gets `404`, `public.key` included. See [Deb, RPM and pacman groups](../repositories/hosted-proxy-group.md#deb-rpm-and-pacman-groups).
- **Regeneration.** The server regenerates the indexes in the background when a hosted member changes (upload, delete, trash, restore, cleanup), when the group's members or settings change, when a member is deleted or blocked, and at startup for groups that have no index yet.
- **Proxy members** have no change signal. A request that finds the indexes older than the shortest `cache_ttl_seconds` among the proxies (at least one minute) serves them and refreshes them in the background.
- **Unreachable proxy.** The server leaves a proxy that cannot be reached, or whose index is over 128 MiB, out of that run. It retries after a minute. A directory that only that proxy had keeps its old index. A proxy that does not have a directory is a complete answer.
- **Minimum age and curation** of a proxy member apply as described under Proxy.
- A group is read-only: uploads answer `405`.

## Limits and behaviour

- **Upload size:** 1 GiB per package; larger uploads answer `413`.
- **Index caching:** `public, max-age=60` for anonymous reads of a public repository, `private, no-cache` otherwise.
- **Delete:** moves the package to the [trash](../operations/trash.md).
- **Status codes:** `400` invalid package or path, `409` existing name and version, `404` unknown file or missing index.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `UNTRUSTED signature` | The public key is not in `/etc/apk/keys`, or its file name differs from the key name in the index. | Install `public.key` under the `key_name` from the `apk-key` API. Reinstall it after a rotation, import or rebuild. |
| `BAD signature` | The key file does not match the signer (an old key after rotation), or a proxy or cache altered the index. | Reinstall the current public key and clear intermediate caches. |
| `APKINDEX.tar.gz` is `404` right after the first upload | The index is still being generated. | Retry in a moment. |
| `404` for `public.key` | The repository has no key because `OPENARTIFACT_SECRET_KEY` is not set, or it is a proxy. | Set the secret on a hosted repository or group. A proxy needs no key. |
| `401` or `403` from `apk update` | Private repository without credentials. | Put credentials in the URL; the token needs read permission. |
| Upload `400` | Not a valid v2 `.apk`, arch directory differs from the package architecture, or the file name is not `<name>-<version>.apk`. | Fix the package or the path. |
| Upload `409` | That name and version already exist in the directory. | Delete the old one or bump the release. |
