<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Arch Linux (pacman)

Hosted pacman repository: upload `.pkg.tar.zst` packages and pacman reads the generated `.db` and `.files` databases, signed with the repository's GPG key.

| | |
|---|---|
| Format id | `pacman` |
| Repo URL | `HOST/repos/REPO/os/<arch>` (databases and packages live in the same directory) |
| Repository types | hosted, proxy ([Proxy](#proxy)), group ([Group](#group)) |

## Create a repository

Create a repository with format `pacman` (web UI: format **Arch Linux (pacman)**; API or CLI: `format: pacman`, see [repositories](../repositories/hosted-proxy-group.md)). To have the databases and the packages signed, give the repository a GPG key first (see [Signing](#signing)).

## Client setup

`internal/clientsetup` produces the `pacman.conf` section, the key import and the upload command:

```ini
# /etc/pacman.conf
[REPO]
SigLevel = Required
Server = HOST/repos/REPO/os/$arch
```

For a private repository put an API token in the URL (HTTP Basic; the user name is ignored, the token is the password):

```ini
[REPO]
SigLevel = Required
Server = https://USER:TOKEN@HOST/repos/REPO/os/$arch
```

Public repositories need no credentials.

Trust the repository key once, then refresh the databases:

```sh
curl -fsSL HOST/repos/REPO/public.key -o public.key
sudo pacman-key --add public.key
sudo pacman-key --lsign-key FINGERPRINT   # fingerprint of the key in public.key
sudo pacman -Sy
```

Without a signing key `public.key` and the `.sig` files answer `404`; set `SigLevel = Optional` (or `Never`) in the repository section, only on networks you control.

## Publish

`PUT /repos/REPO/os/ARCH/FILE` with the raw package as the request body. Needs write permission on `REPO` ([RBAC](../security/rbac.md)).

```sh
curl -fsS -H "Authorization: Bearer $OA_TOKEN" \
  -T my-package-1.0-1-x86_64.pkg.tar.zst \
  HOST/repos/REPO/os/x86_64/my-package-1.0-1-x86_64.pkg.tar.zst

# optional detached signature, after the package
curl -fsS -H "Authorization: Bearer $OA_TOKEN" \
  -T my-package-1.0-1-x86_64.pkg.tar.zst.sig \
  HOST/repos/REPO/os/x86_64/my-package-1.0-1-x86_64.pkg.tar.zst.sig
```

The CLI does the same with the repository's address derived from the package file name: `oa upload REPO my-package-1.0-1-x86_64.pkg.tar.zst [my-package-1.0-1-x86_64.pkg.tar.zst.sig]` ([CLI](../integrations/cli.md)).

Rules:

- **File name.** Packages are `.pkg.tar.zst`, `.pkg.tar.xz` or `.pkg.tar.gz` and must be named as makepkg wrote them: `name-[epoch:]version-pkgrel-arch.pkg.tar.EXT`. A hyphen in place of the epoch colon (`name-1-2.0-1-x86_64...`) is accepted as well.
- **`.PKGINFO` is parsed and verified.** The name, version and architecture in the package must agree with the file name and with the `ARCH` directory it is uploaded to. An unreadable package is rejected.
- **Architecture directory.** `ARCH` must equal the package's `arch`. An `any` package is uploaded to the directory of any architecture the repository serves (for example `x86_64`); it is then listed in the database of every architecture directory the repository has. `os/any/` itself is rejected.
- **Packages are immutable.** Uploading a file that already exists is `409`. Delete it first (below) to replace it.
- **Signature.** `PUT .../PACKAGE.sig` after the package stores a detached signature (ASCII-armored or binary; it is stored and served as binary). `DELETE` on the package removes it; `DELETE` on an uploader `.sig` removes that signature only.
- Quota, [license policy](../security/scanning.md#blocking-licenses), [webhooks](../operations/webhooks.md) and the [trash](../operations/trash.md) apply as for the other formats: a delete is soft (trash kind `pacman`) and can be restored.
- The databases cannot be uploaded; they are generated. `repo-add` against the server is not supported, upload the packages instead.

The web UI's upload zone accepts packages and `.sig` files; the repository's artifact pages and browse view list them, and the connect panel shows the snippets above.

## Generated databases

Served relative to `HOST/repos/REPO/os/ARCH/`:

| Path | Notes |
|---|---|
| `<name>.db`, `<name>.db.tar.gz` | package database (gzip tarball in `repo-add` layout) |
| `<name>.files`, `<name>.files.tar.gz` | database that also lists the files of every package |
| each of the above with `.sig` | detached signature, only when a signing key applies |
| `<package file>` | package download; `Range` requests are supported so pacman can resume |

- Any file name before the extension works: `REPO.db` and `core.db` both resolve to the database of `REPO`.
- **Eventually consistent.** The databases are regenerated in the background after every upload and delete; requests in quick succession are coalesced. A few seconds after the first upload `pacman -Sy` works. Before the first generation the database answers `404`.
- `If-Modified-Since` on a database answers `304` when it has not changed.
- Entries carry the MD5 and SHA-256 sums and the `%PGPSIG%` of the package.
- Not supported: `.db.tar.zst` or `.xz` databases and delta packages.

## Retention: what is listed

- Only the **newest version per package name** is listed in the database, compared with pacman's `vercmp` (epoch, then version, then pkgrel). Older files stay downloadable by file name, so `pacman -U URL` and downgrades by URL keep working, but `pacman -S` installs the newest.
- Packages with architecture `any` are listed in the database of every architecture directory of the repository.
- Packages in the trash are not listed and answer `404`.

## Signing

The databases are signed with the repository's GPG key, resolved like for Debian and RPM repositories: the key activated for the repository, else the global active key, else the configured key file. Manage keys under **Settings > GPG keys** (or `oa gpg`, the [admin API](../reference/api/gpg.md)); repositories of format `pacman` appear in the key assignment. See [Signing](../security/signing.md).

- With a key: `.db.sig`, `.files.sig` and the package signatures are served, and `HOST/repos/REPO/public.key` returns the public key.
- **Server-signed packages.** A package uploaded without a `.sig` is signed by the server with the same key. Those signatures are re-created when the key is rotated and removed when the key is removed. A signature you uploaded is kept as it is and never replaced.
- Without a key the databases are unsigned and clients need `SigLevel = Optional` or `Never`.

Import the public key on every client:

```sh
curl -fsSL HOST/repos/REPO/public.key -o public.key
sudo pacman-key --add public.key
sudo pacman-key --lsign-key FINGERPRINT
```

## Proxy

A pacman proxy mirrors the **root** of an Arch mirror by path. `upstream_url` defaults to `https://geo.mirror.pkgbuild.com`; any mirror root works, and a custom upstream with a path prefix works too ([creating repositories](../repositories/hosted-proxy-group.md#creating-repositories)). One proxy covers every repository (`core`, `extra`, ...) and architecture of the mirror, because paths pass through unchanged.

[Package curation](../repositories/hosted-proxy-group.md#package-curation) refuses the download of a blocked package file (`403` with the reason, even when cached): rules name the package and a version as `<version>-<pkgrel>` from the file name `<name>-<version>-<pkgrel>-<arch>.pkg.tar.<ext>` (an epoch is written as the mirror shows it, `1:2.0-1`). Sync databases and `.sig` files are served as the upstream has them, so pacman still lists a blocked package.

Put the proxy at the top of `/etc/pacman.d/mirrorlist`:

```
Server = HOST/repos/NAME/$repo/os/$arch
```

For a private proxy use `Server = https://USER:TOKEN@HOST/repos/NAME/$repo/os/$arch` (HTTP Basic, the token is the password).

- **Signatures are the upstream's.** Databases and packages are verified with the keys of `archlinux-keyring`; there is nothing to import from OpenArtifact and `public.key` is not used.
- **Databases** (`.db`, `.files` and their `.sig`) are cached for at most 5 minutes, whatever the repository's `cache_ttl_seconds` (a lower value is honoured), and then revalidated against the upstream. The cap exists because mirrors drop superseded package versions, and a longer-lived database would list files the upstream no longer has. `If-Modified-Since` is answered with `304` against the time the proxy last refreshed the file.
- **Packages** are cached on the first request. They are not pinned as immutable: they follow the repository's `cache_ttl_seconds` (default 3600, see [hosted, proxy, group](../repositories/hosted-proxy-group.md)), because third-party mirrors may republish a file name. Upstream `.sig` files pass through.
- **No resumed downloads.** The proxy does not support `Range`: a resumed or partial request receives the full file.
- Proxy repositories are read-only: `PUT` and `DELETE` answer `405`. Negative caching and serve-stale behave as for other proxies.

## Group

A pacman group serves several repositories as one: hosted repositories of your own and proxies of Arch mirror sections. It is read-only and has the layout of a hosted repository, so the client setup is that of [a hosted repository](#client-setup) without publishing:

```
[GROUP]
SigLevel = Required
Server = https://HOST/repos/GROUP/os/$arch
```

The group's name is the database name (`GROUP.db`); import the group's key from `HOST/repos/GROUP/public.key` as shown above.

- **Members.** Hosted pacman repositories, and pacman proxies whose `upstream_url` names a **section** of a mirror (`https://geo.mirror.pkgbuild.com/extra`), because the group reads `os/<arch>/<section>.db` below it. A proxy of a mirror root (the default `https://geo.mirror.pkgbuild.com`) has no section: it works as a standalone proxy but contributes nothing to a group (a log line says so). To mix several Arch sections add one proxy per section.
- **Databases.** For each architecture the group publishes `os/<arch>/GROUP.db` and `GROUP.files` with their `.sig`, merged from the members' databases of that architecture and signed with the group's key (the server-wide key, as for hosted repositories; unsigned without one). An architecture is generated when it is first requested, if some member has packages for it (`404` otherwise), and regenerated in the background when a hosted member changes, the members change, or a proxy member's cache expires. `any` packages of a hosted member are listed in every architecture the group serves.
- **One version per name.** A pacman database lists a single version of a name. Hosted wins: a name held by a hosted member is never taken from a proxy member, whatever the member order; among the others the first member in order that has the name wins. With `merge_proxy_versions` the newest version (pacman's `vercmp`; a tie goes to the earlier member) wins instead.
- **Packages and `.sig` files** are served from the member that owns the file listed in the merged database; a file the database does not list is `404`.
- **Files database.** For hosted members it lists their files. For proxy members it is read best-effort from the upstream's `<section>.files` (at most 256 MiB of file lists in total are kept); packages for which none was read are listed without files.
- **Trust.** The group does not re-sign packages: each database entry carries the signature of its package (`%PGPSIG%`), so with `SigLevel = Required` the client must trust the signing key of every member whose packages it takes. Hosted repositories sign with the server-wide key unless they have their own ([signing](../security/signing.md)); the server-wide key is also the group's key, so one `pacman-key --add` covers them. A hosted member with its own key needs that key imported too (`HOST/repos/MEMBER/public.key`), and packages of an Arch mirror section are signed by Arch packagers: the client needs `archlinux-keyring`, as for the mirror itself.
- Access control, regeneration and partial runs work as for [deb and RPM groups](../repositories/hosted-proxy-group.md#deb-rpm-and-pacman-groups).

## Not supported

Building packages, the AUR, running `repo-add` against the server (upload packages instead), the `any` architecture directory, delta packages and `.db.tar.zst` databases.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `pacman -Sy` fails with `404` on `REPO.db` | Nothing was uploaded yet, or the first database generation has not finished. Upload a package, wait a few seconds, retry. |
| `error: failed retrieving file 'REPO.db.sig'` / `invalid or corrupted database (PGP signature)` | The key is not trusted on the client (`pacman-key --add` and `--lsign-key`, see above), the repository has no signing key (no `.sig` exists: add a key or lower `SigLevel`), or the key was rotated and the client still has the old one. |
| `signature from "..." is unknown trust` | The key is imported but not locally signed: run `pacman-key --lsign-key FINGERPRINT`. |
| `PUT` answers `400` about the architecture | The `ARCH` directory differs from the package's `arch`, or you used `os/any/`. Upload to the directory of the real architecture (for an `any` package, any real one such as `x86_64`). |
| `PUT` answers `400` about the file name | The name must be `name-[epoch:]version-pkgrel-arch.pkg.tar.EXT`, as makepkg wrote it, and agree with `.PKGINFO`. |
| `PUT` answers `409` | The file already exists; packages are immutable. `DELETE` it first, or bump `pkgrel`. |
| `401`/`403` on download | Private repository: use `Server = https://USER:TOKEN@HOST/repos/REPO/os/$arch` with an API token. |
| A new version is not installed by `pacman -S` | The database is regenerated in the background; wait a few seconds and run `pacman -Sy` again. |
