<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# RPM (YUM/DNF)

YUM/DNF repository with generated `repodata/`, optional `comps.xml` groups and GPG-signed metadata.

| | |
|---|---|
| Format id | `rpm` |
| Repo URL | `HOST/repos/REPO` (metadata `repodata/repomd.xml`, packages `Packages/`) |
| Repository types | hosted, proxy; no groups ([details](../repositories/hosted-proxy-group.md)) |

## Client setup

`internal/clientsetup` produces:

```ini
# /etc/yum.repos.d/REPO.repo
[REPO]
name=REPO
baseurl=HOST/repos/REPO
enabled=1
repo_gpgcheck=1
gpgkey=HOST/repos/REPO/public.key
```

```sh
curl -u USERNAME:<TOKEN> -F file=@my-package-1.0.x86_64.rpm HOST/api/v1/repositories/REPO/packages
```

`repomd.xml.asc` and `public.key` exist only when signing is enabled (`GPG_ENABLED=true` and `GPG_KEY_PATH` set; see [Signing](#signing)). Without signing set `repo_gpgcheck=0` and drop `gpgkey`. For private repositories add `username=` and `password=<TOKEN>` to the repo file (the username is ignored).

## Upload API

`POST /api/v1/repositories/REPO/packages` with multipart field `file`. Needs write permission on `REPO` ([RBAC](../security/rbac.md)).

- The part's Content-Type must be empty, `application/x-rpm` or `application/octet-stream`. An unreadable package is `400 invalid rpm package: ...`; a non-`rpm` repository is `400`; a proxy repository `405`.
- Maximum 500 MiB; quota is enforced ([quotas](../repositories/cleanup-and-quotas.md)).
- Header limits: at most 65535 header index entries and 100000 elements per array tag. A package that declares more dependencies (requires, provides, ...) or more index entries is `400 invalid rpm package: ...`. A longer file list is accepted but truncated: at most 100000 files and 16 MiB of path strings are stored, so `filelists.xml` lists a prefix of the package's files (the package itself is stored whole and installs normally).
- The same NEVRA, or the same SHA-256 in the repository, is `409 CONFLICT`: packages are immutable. The stored name is `name-version-release.arch.rpm`.
- `DELETE /api/v1/repositories/REPO/packages/<id>` deletes one package by id (only if it belongs to `REPO`).
- `PUT /api/v1/repositories/REPO/comps` uploads a `comps.xml` (max 10 MiB, validated as comps XML). It is served at `repodata/comps.xml` and listed in `repomd.xml` as a `group` entry.

## Generated metadata

Served relative to `HOST/repos/REPO`:

| Path | Notes |
|---|---|
| `repodata/repomd.xml` | index of the files below |
| `repodata/<checksum>-primary.xml.gz` / `.xz`, `-filelists`, `-other` | one gzip and one xz copy of each |
| `repodata/comps.xml` | only after a comps upload |
| `repodata/repomd.xml.asc`, `public.key` | only with signing enabled |
| `Packages/<file>.rpm` | package download (`application/x-rpm`, `Cache-Control: max-age=3600`); honours quarantine |

- A background worker regenerates metadata after changes. Until the first generation `repomd.xml` answers `503` with `Retry-After: 5` and `Cache-Control: no-store`.
- At startup the server regenerates repositories flagged as stale.
- The first upload creates the repository's RPM record with base architecture `x86_64`.

## Signing

Set `GPG_ENABLED=true`, `GPG_KEY_PATH` and `GPG_PASSPHRASE` ([signing](../security/signing.md)): the worker then writes `repomd.xml.asc` and `public.key`, which `repo_gpgcheck=1` and `gpgkey=` use. `repomd.xml.asc` is a standard ASCII-armored detached signature (`gpg --verify repomd.xml.asc repomd.xml` succeeds with the key from `public.key`). Package files themselves are not re-signed.

`primary.xml` lists each package's installed size (`RPMTAG_LONGSIZE`/`SIZE`) and archive size (rpm 4 signature tag 271/1007, or `PAYLOADSIZEALT` for rpm 6 packages), which dnf uses for its transaction size checks. Packages uploaded by earlier versions are re-read from storage the first time their repository's metadata is regenerated, which the upgrade triggers once at startup.

## Proxy

An RPM proxy repository mirrors `repodata/` and `Packages/` from `upstream_url`. `.rpm` files below a `Packages/` directory are immutable once cached; `.rpm` files elsewhere (flat layouts) and `repodata/` are served from cache until `cache_ttl_seconds` (default 1 hour) and then revalidated. Upstream metadata and signatures pass through unchanged, so clients must trust the upstream's key. Negative caching and serve-stale: [hosted, proxy, group](../repositories/hosted-proxy-group.md).
