<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Arch Linux (pacman)

Use a pacman repository to publish `.pkg.tar.zst` packages for Arch Linux, mirror an Arch mirror with a proxy, or combine repositories in a group. The server generates the `.db` and `.files` databases and signs them with the repository's GPG key.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO/os/<arch>` (databases and packages share the directory) |
| Format id | `pacman` |
| Repository types | hosted, proxy ([Proxy](#proxy)), group ([Group](#group)) |
| Default upstream (proxy) | `https://geo.mirror.pkgbuild.com` |

## Create a repository

Create a repository with format `pacman`. In the web UI choose **Arch Linux (pacman)**. In the API or CLI use `format: pacman`; see [repositories](../repositories/hosted-proxy-group.md). To have databases and packages signed, give the repository a GPG key first; see [Signing](#signing).

## Client setup

Add a section to `/etc/pacman.conf`. The web UI connect panel shows the same snippets with your own URL.

```ini
[REPO]
SigLevel = Required
Server = HOST/repos/REPO/os/$arch
```

For a private repository put an API token in the URL. The user name is ignored; the token is the password.

```ini
[REPO]
SigLevel = Required
Server = https://<USERNAME>:<TOKEN>@HOST/repos/REPO/os/$arch
```

Trust the repository key once, then refresh the databases:

```sh
curl -fsSL HOST/repos/REPO/public.key -o public.key
sudo pacman-key --add public.key
sudo pacman-key --lsign-key FINGERPRINT   # fingerprint of the key in public.key
sudo pacman -Sy
```

Without a signing key, `public.key` and the `.sig` files answer `404`. Then set `SigLevel = Optional` (or `Never`) in the repository section, only on networks you control.

## Publish

Upload with `PUT /repos/REPO/os/ARCH/FILE` and the raw package as the body. You need write permission on `REPO` ([RBAC](../security/rbac.md)).

```sh
curl -fsS -H "Authorization: Bearer <TOKEN>" \
  -T my-package-1.0-1-x86_64.pkg.tar.zst \
  HOST/repos/REPO/os/x86_64/my-package-1.0-1-x86_64.pkg.tar.zst

# optional detached signature, after the package
curl -fsS -H "Authorization: Bearer <TOKEN>" \
  -T my-package-1.0-1-x86_64.pkg.tar.zst.sig \
  HOST/repos/REPO/os/x86_64/my-package-1.0-1-x86_64.pkg.tar.zst.sig
```

The CLI derives the address from the file name:

```sh
oa upload REPO my-package-1.0-1-x86_64.pkg.tar.zst [my-package-1.0-1-x86_64.pkg.tar.zst.sig]
```

See [CLI](../integrations/cli.md). The web UI upload zone accepts packages and `.sig` files.

Rules:

- **File name.** Packages are `.pkg.tar.zst`, `.pkg.tar.xz` or `.pkg.tar.gz`, named as makepkg wrote them: `name-[epoch:]version-pkgrel-arch.pkg.tar.EXT`. A hyphen in place of the epoch colon (`name-1-2.0-1-x86_64...`) is accepted as well.
- **`.PKGINFO` is checked.** The name, version and architecture in the package must agree with the file name and with the `ARCH` directory. An unreadable package is rejected.
- **Architecture directory.** `ARCH` must equal the package's `arch`. An `any` package is uploaded to the directory of any architecture the repository serves, for example `x86_64`. `os/any/` itself is rejected.
- **Size.** A package may be up to 8 GiB. A larger upload is `413 upload too large`.
- **Immutable.** Uploading a file that exists is `409`. Delete it first to replace it.
- **Signature.** `PUT .../PACKAGE.sig` after the package stores a detached signature, ASCII-armored or binary. It is stored and served as binary.
- **Deleting.** `DELETE` on the package removes it and its signature. `DELETE` on an uploaded `.sig` removes only that signature. A delete is soft: the package goes to the [trash](../operations/trash.md) (kind `pacman`) and can be restored.
- **Other checks.** Quota, [license policy](../security/scanning.md#blocking-licenses) and [webhooks](../operations/webhooks.md) apply as for other formats.
- **Databases.** They cannot be uploaded. `repo-add` against the server is not supported; upload the packages instead.

## Install and consume

Files served relative to `HOST/repos/REPO/os/ARCH/`:

| Path | Notes |
|---|---|
| `<name>.db`, `<name>.db.tar.gz` | package database |
| `<name>.files`, `<name>.files.tar.gz` | database that also lists the files of every package |
| each of the above with `.sig` | detached signature, only when a signing key applies |
| `<package file>` | package download; `Range` requests are supported so pacman can resume |

- Any file name before the extension works: `REPO.db` and `core.db` both resolve to the database of `REPO`.
- **Eventually consistent.** The databases are regenerated in the background after every upload and delete. A few seconds after the first upload `pacman -Sy` works. Before the first generation the database answers `404`.
- `If-Modified-Since` on a database answers `304` when it has not changed.
- Entries carry the MD5 and SHA-256 sums and the PGP signature of the package.
- Not supported: `.db.tar.zst` or `.xz` databases and delta packages.

### Which versions are listed

- Only the newest version per package name is listed, compared as pacman does (epoch, then version, then pkgrel).
- Older files stay downloadable by file name, so `pacman -U URL` and downgrades by URL keep working. `pacman -S` installs the newest.
- Packages with architecture `any` are listed in the database of every architecture directory of the repository.
- Packages in the trash are not listed and answer `404`.

## Signing

The databases are signed with the repository's GPG key. The key is resolved in this order: the key activated for the repository, the global active key, the configured key file. Manage keys under **Settings > GPG keys**, with `oa gpg`, or with the [admin API](../reference/api/gpg.md). Repositories of format `pacman` appear in the key assignment. See [Signing](../security/signing.md).

- **With a key.** `.db.sig`, `.files.sig` and the package signatures are served. `HOST/repos/REPO/public.key` returns the public key.
- **Server-signed packages.** A package uploaded without a `.sig` is signed by the server with the same key. Those signatures are re-created on key rotation and removed when the key is removed. A signature you uploaded is kept and never replaced.
- **Without a key.** The databases are unsigned. Clients need `SigLevel = Optional` or `Never`.

Import the public key on every client as shown in [Client setup](#client-setup).

## Proxy

Create a proxy with `repo_type: "proxy"`. It mirrors the root of an Arch mirror by path. `upstream_url` defaults to `https://geo.mirror.pkgbuild.com`. Any mirror root works, including one with a path prefix; see [creating repositories](../repositories/hosted-proxy-group.md#creating-repositories). One proxy covers every repository (`core`, `extra`, ...) and architecture, because paths pass through unchanged.

Put the proxy at the top of `/etc/pacman.d/mirrorlist`:

```
Server = HOST/repos/NAME/$repo/os/$arch
```

For a private proxy use the token in the URL:

```
Server = https://<USERNAME>:<TOKEN>@HOST/repos/NAME/$repo/os/$arch
```

- **Signatures are the upstream's.** Databases and packages are verified with the keys of `archlinux-keyring`. There is nothing to import from OpenArtifact, and `public.key` is not used.
- **Databases.** `.db`, `.files` and their `.sig` are cached for at most 5 minutes, then revalidated. A lower `cache_ttl_seconds` is honoured. The cap exists because mirrors drop old package versions, and a longer-lived database would list files the upstream no longer has.
- **`If-Modified-Since`.** It is answered with `304` against the time the proxy last refreshed the file.
- **Packages.** They are cached on first request and follow the repository's `cache_ttl_seconds` (default 3600), not pinned as immutable, because third-party mirrors may republish a file name. Upstream `.sig` files pass through.
- **No resumed downloads.** The proxy does not support `Range`. A resumed request receives the full file.
- **Read-only.** `PUT` and `DELETE` answer `405`. Negative caching and serve-stale behave as for other proxies; see [hosted, proxy, group](../repositories/hosted-proxy-group.md).

### Minimum age and curation

Curation applies to package downloads. Minimum age has no effect, because mirrors give no publish date per version; see [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation).

- **Names.** Rules match the name from the file name `<name>-<version>-<pkgrel>-<arch>.pkg.tar.<ext>`, and the version as `<version>-<pkgrel>`. An epoch is written as the mirror shows it, for example `1:2.0-1`.
- **Blocked download.** A blocked package file answers `403` with the reason, even when cached.
- **Not gated.** Sync databases and `.sig` files are served as the upstream has them, so pacman still lists a blocked package.

## Group

A pacman group serves several repositories as one: hosted repositories of your own and proxies of Arch mirror sections. It is read-only and has the layout of a hosted repository.

```ini
[GROUP]
SigLevel = Required
Server = https://HOST/repos/GROUP/os/$arch
```

The group's name is the database name (`GROUP.db`). Import the group's key from `HOST/repos/GROUP/public.key` as in [Client setup](#client-setup).

- **Members.** Hosted pacman repositories, and pacman proxies whose `upstream_url` names a section of a mirror, such as `https://geo.mirror.pkgbuild.com/extra`. The group reads `os/<arch>/<section>.db` below it.
- **Whole-mirror proxies.** A proxy of a mirror root has no section. It works as a standalone proxy but contributes nothing to a group; a log line says so. To mix several Arch sections, add one proxy per section.
- **Databases.** For each architecture the group publishes `os/<arch>/GROUP.db` and `GROUP.files` with their `.sig`. They are merged from the members' databases and signed with the server-wide key, or unsigned without one.
- **When generated.** An architecture is generated when first requested, if some member has packages for it (`404` otherwise). It is regenerated in the background when a hosted member or the members change, or when a proxy member's cache expires.
- **`any` packages.** Those of a hosted member are listed in every architecture the group serves.
- **One version per name.** A database lists a single version of a name. A name held by a hosted member is never taken from a proxy member, whatever the member order; see [hosted members win](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name). Among the others, the first member in order wins.
- **`merge_proxy_versions`.** With it set, the newest version wins instead. A tie goes to the earlier member.
- **Downloads.** Packages and `.sig` files come from the member that owns the file listed in the merged database. A file the database does not list is `404`.
- **Files database.** For hosted members it lists their files. For proxy members it is read on a best-effort basis from the upstream's `<section>.files`. The group keeps at most 256 MiB of that data per request, shared by all members; packages without a file list are listed without files.
- **Trust.** The group does not re-sign packages. Each entry carries the signature of its package, so with `SigLevel = Required` the client must trust the key of every member whose packages it takes.
- **Keys.** Hosted repositories sign with the server-wide key unless they have their own, and that key is also the group's key, so one `pacman-key --add` covers them. A hosted member with its own key needs `HOST/repos/MEMBER/public.key` imported too. Arch packages need `archlinux-keyring`.

Access control, regeneration and partial runs work as for [Deb, RPM and pacman groups](../repositories/hosted-proxy-group.md#deb-rpm-and-pacman-groups).

## Limits and behaviour

- **Upload size.** 8 GiB per package (`413 upload too large`).
- **Databases.** Hosted databases are regenerated after each change; proxy databases are cached up to 5 minutes.
- **Deleting.** A delete moves the package to the trash and it disappears from the next database.
- **Not supported.** Building packages, the AUR, `repo-add` against the server, the `any` architecture directory, delta packages and `.db.tar.zst` databases.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `pacman -Sy` fails with `404` on `REPO.db` | Nothing was uploaded yet, or the first database generation has not finished. Upload a package, wait a few seconds, retry. |
| `failed retrieving file 'REPO.db.sig'` or `invalid or corrupted database (PGP signature)` | The key is not trusted on the client, the repository has no signing key (no `.sig` exists), or the key was rotated. Run `pacman-key --add` and `--lsign-key`, add a key, or lower `SigLevel`. |
| `signature from "..." is unknown trust` | The key is imported but not locally signed. Run `pacman-key --lsign-key FINGERPRINT`. |
| `PUT` answers `400` about the architecture | The `ARCH` directory differs from the package's `arch`, or you used `os/any/`. Upload to the real architecture directory; for an `any` package use any real one such as `x86_64`. |
| `PUT` answers `400` about the file name | The name must be `name-[epoch:]version-pkgrel-arch.pkg.tar.EXT` and agree with `.PKGINFO`. |
| `PUT` answers `409` | The file exists. `DELETE` it first, or bump `pkgrel`. |
| `PUT` answers `413` | The package is over 8 GiB. |
| `401` or `403` on download | Private repository. Use `Server = https://<USERNAME>:<TOKEN>@HOST/repos/REPO/os/$arch` with an API token. |
| A new version is not installed by `pacman -S` | The database regenerates in the background. Wait a few seconds and run `pacman -Sy` again. |
| A group lacks packages of a proxy | The proxy points at a mirror root, not a section. Use one proxy per section URL. |
