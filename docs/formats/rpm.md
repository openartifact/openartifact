<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# RPM (YUM/DNF)

Use an RPM repository to publish `.rpm` packages for dnf, yum and rpm-ostree, mirror an upstream repository with a proxy, or combine repositories in a group. The server generates `repodata/`, optional `comps.xml` groups and GPG-signed metadata.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO` (metadata at `repodata/repomd.xml`, packages under `Packages/`) |
| Format id | `rpm` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | none; `upstream_url` is required |

## Client setup

Create the repo file. The web UI connect panel shows the same snippet with your own URL.

```ini
# /etc/yum.repos.d/REPO.repo
[REPO]
name=REPO
baseurl=HOST/repos/REPO
enabled=1
repo_gpgcheck=1
gpgkey=HOST/repos/REPO/public.key
```

`repomd.xml.asc` and `public.key` exist only when a signing key applies to the repository. That is a managed key from the admin API or `oa gpg`, or `GPG_ENABLED=true` with `GPG_KEY_PATH`; see [Signing](#signing). If the repository has no signing key, set `repo_gpgcheck=0` and drop `gpgkey`.

For a private repository add the token to the repo file. The user name is ignored.

```ini
username=<USERNAME>
password=<TOKEN>
```

## Publish

<a id="upload-api"></a>

Upload a package with `POST /api/v1/repositories/REPO/packages` and the multipart field `file`. You need write permission on `REPO` ([RBAC](../security/rbac.md)).

```sh
curl -u <USERNAME>:<TOKEN> -F file=@my-package-1.0.x86_64.rpm HOST/api/v1/repositories/REPO/packages
```

- The part's Content-Type must be empty, `application/x-rpm` or `application/octet-stream`.
- An unreadable package is `400 invalid rpm package: ...`. A non-`rpm` repository is `400`. A proxy repository is `405`.
- The limit is 500 MiB. The [quota](../repositories/cleanup-and-quotas.md) is enforced.
- The same NEVRA, or the same SHA-256 in the repository, is `409 CONFLICT`. Packages are immutable. The stored name is `name-version-release.arch.rpm`.
- `DELETE /api/v1/repositories/REPO/packages/<id>` deletes one package by id, only if it belongs to `REPO`.
- There is no `/repos/REPO/upload` route. `POST` or `PUT` there answers `404` with a message naming the correct endpoint.

Header limits protect the server:

- At most 65535 header index entries and 100000 elements per array tag. A package that declares more is `400 invalid rpm package: ...`.
- A longer file list is accepted but truncated to 100000 files and 16 MiB of path strings. `filelists.xml` then lists a prefix of the files. The package itself is stored whole and installs normally.

To add package groups, upload a `comps.xml` with `PUT /api/v1/repositories/REPO/comps` (at most 10 MiB, validated as comps XML). It is served at `repodata/comps.xml` and listed in `repomd.xml` as a `group` entry.

## Install and consume

Clients read the generated metadata, relative to `HOST/repos/REPO`:

| Path | Notes |
|---|---|
| `repodata/repomd.xml` | index of the files below |
| `repodata/<checksum>-primary.xml.gz` / `.xz`, `-filelists`, `-other` | one gzip and one xz copy of each |
| `repodata/comps.xml` | only after a comps upload |
| `repodata/repomd.xml.asc`, `public.key` | only with signing enabled |
| `Packages/<file>.rpm` | package download (`application/x-rpm`); honours quarantine |

- A background worker regenerates metadata after changes. The server also regenerates repositories flagged as stale at startup.
- Until the first generation, `repomd.xml` answers `503` with `Retry-After: 5` and `Cache-Control: no-store`.
- The first upload creates the repository's RPM record with base architecture `x86_64`.
- `primary.xml` lists each package's installed size and archive size, which dnf uses for its transaction size checks. Packages stored by older releases are refreshed by a metadata regeneration; see [upgrading](../getting-started/upgrading.md).

## Signing

Set `GPG_ENABLED=true`, `GPG_KEY_PATH` and `GPG_PASSPHRASE`; see [signing](../security/signing.md). The worker then writes `repomd.xml.asc` and `public.key`, which `repo_gpgcheck=1` and `gpgkey=` use.

`repomd.xml.asc` is a standard ASCII-armored detached signature. This succeeds with the key from `public.key`:

```sh
gpg --verify repomd.xml.asc repomd.xml
```

### Signing packages (`sign_packages`)

`repo_gpgcheck=1` proves only that the metadata is genuine. dnf and yum default to `gpgcheck=1`, which also wants a signature inside each `.rpm`.

Turn on **Sign uploaded packages** in the repository settings, or use one of these. It applies to hosted `rpm` repositories only and is off by default.

```sh
oa repo update NAME --sign-packages
```

```
PATCH /api/v1/repository/NAME   {"sign_packages": true}
```

Every upload then gets an OpenPGP header signature from the key that signs the repository's metadata: repository key, then global key, then the configured key file. Clients configure:

```ini
gpgcheck=1
repo_gpgcheck=1
gpgkey=HOST/repos/REPO/public.key
```

How it behaves:

- **The signed file is the stored file.** Signing rewrites the RPM signature header, so the result is a new file with its own SHA-256 and size. Only that file is kept. The package's size, `primary.xml` checksum, `repomd.xml` and the API all describe the bytes clients download.
- **Payload untouched.** `rpm -K` reports the digests and the signature as OK.
- **Original upload is a duplicate.** Uploading the unsigned original again is `409` (same NEVRA). Keep the unsigned file if you need it elsewhere.
- **No key, no upload.** If `sign_packages` is on and no key applies, the upload fails with `409 SIGNING_KEY_REQUIRED`. Nothing is stored unsigned.
- **Unsignable package.** A package that cannot be signed is `422 SIGNING_FAILED`. The signer needs the payload digest that rpm 4.14 and later writes, and the rpm 4 package format. Packages built with `_rpmformat 6` are not supported.
- **Existing packages are not re-signed.** Turning the setting on affects later uploads. After a key rotation, keep the old public key in the clients' keyring (`rpm --import`), or delete and re-upload the packages.

Debian repositories need nothing equivalent: apt verifies the signed `Release`, which covers every package through its checksums.

## rpm-ostree and package layering

`rpm-ostree` reads the same `.repo` files as dnf. Tested by hand against a hosted repository with `sign_packages` on, `gpgcheck=1` and `repo_gpgcheck=1`: dnf 5 and `rpm-ostree compose install` read the metadata, verify signatures and install the package.

- **Private repository.** The `.repo` file needs `username=` and `password=` (a token) for metadata and packages. libdnf does not send them when it fetches `gpgkey=`, so that URL answers `401`. Download the key and point at the file, or make the repository public:

```sh
curl -u <USERNAME>:<TOKEN> -O HOST/repos/REPO/public.key
```

```ini
gpgkey=file:///path/public.key
```

- **Dependencies.** The depsolver sees only enabled repositories. A package whose dependencies are in another repository needs that one enabled too.

## Proxy

Create a proxy with `repo_type: "proxy"` and an `upstream_url`. It mirrors `repodata/` and `Packages/` from the upstream. Use the proxy's URL as `baseurl`.

- **Immutable files.** `.rpm` files below a `Packages/` directory are cached as immutable.
- **Other files.** `.rpm` files elsewhere (flat layouts) and `repodata/` are served from cache until `cache_ttl_seconds` (default 1 hour), then revalidated.
- **Signatures.** Upstream metadata and signatures pass through unchanged, so clients must trust the upstream's key. `sign_packages` is rejected for a proxy.
- **Failures.** Negative caching and serve-stale are described in [hosted, proxy, group](../repositories/hosted-proxy-group.md).

### Minimum age and curation

Curation applies to downloads. Minimum age has no effect, because RPM metadata carries no publish date; see [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation).

- **Names.** Rules match the package name from the file name `<name>-<version>-<release>.<arch>.rpm`, and the version as `<version>-<release>`, without an epoch.
- **Blocked download.** A blocked `.rpm` answers `403` with the reason, even when cached.
- **Not gated.** `repodata/` is served as the upstream has it, so dnf still lists a blocked package. `.drpm` delta files are not gated.

## Group

An RPM group is read-only. It publishes merged `repodata/` (`repomd.xml`, `primary`, `filelists`, `other` and `modules`), signed with the server key (`repomd.xml.asc`). It serves `Packages/` files from the member that owns them.

Use `baseurl=HOST/repos/GROUP` and `gpgkey=HOST/repos/GROUP/public.key`, as for a hosted repository.

- A package offered by several members is listed once, from the earliest member.
- A package name held by a hosted member is not offered from proxy members, unless the group has `merge_proxy_versions`; see [hosted members win](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- `comps.xml`, `updateinfo` and the sqlite databases are not merged.

Merging rules, regeneration and access control: [Deb, RPM and pacman groups](../repositories/hosted-proxy-group.md#deb-rpm-and-pacman-groups).

### Proxy over a tree of repositories

A proxy member whose upstream has no `repodata/` at the root, but one per sub-directory such as `9/BaseOS/x86_64/os/`, cannot be merged into the group's index. The group serves such a member's sub-paths unmerged. Use one `baseurl` per repository:

```ini
baseurl=HOST/repos/GROUP/9/BaseOS/x86_64/os/
```

If the group has nothing else to list, `HOST/repos/GROUP/repodata/repomd.xml` answers `404` with a message that says so.

## Limits and behaviour

- **Upload size.** 500 MiB per package; `comps.xml` up to 10 MiB.
- **Not ready.** `repomd.xml` is `503` with `Retry-After: 5` until the first metadata generation.
- **Caching.** Cache-Control is `public, max-age=N` for anonymous reads of a public repository and `private, no-cache` otherwise. Package downloads use `max-age=3600` under the same rule.
- **Immutability.** Packages cannot be replaced. Delete one by id to upload a corrected build.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `repomd.xml` answers `503` | The first metadata generation has not finished. Retry after a few seconds. |
| `404` on `repomd.xml.asc` or `public.key` | No signing key applies. Configure one, or set `repo_gpgcheck=0` and drop `gpgkey`. |
| dnf: package signature missing | The repository does not sign packages. Turn on `sign_packages` or set `gpgcheck=0`. |
| `409 SIGNING_KEY_REQUIRED` on upload | `sign_packages` is on but no key applies. Add a signing key. |
| `422 SIGNING_FAILED` on upload | The package format is not supported by the signer. Rebuild with rpm 4.14 or later using the rpm 4 format. |
| `409 CONFLICT` on upload | The NEVRA or SHA-256 exists. Delete it first, or bump the release. |
| `400 invalid rpm package` | The package is unreadable or has too many header entries or dependencies. |
| `401` on `gpgkey=` for a private repository | libdnf sends no credentials for the key URL. Download the key and use `file://`. |
| `405` on upload | The repository is a proxy or group. Upload to a hosted repository. |
