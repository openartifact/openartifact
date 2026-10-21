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

Set `GPG_ENABLED=true`, `GPG_KEY_PATH` and `GPG_PASSPHRASE` ([signing](../security/signing.md)): the worker then writes `repomd.xml.asc` and `public.key`, which `repo_gpgcheck=1` and `gpgkey=` use. Package files themselves are not re-signed.

## Proxy

An RPM proxy repository mirrors `repodata/` and `Packages/` from `upstream_url`. `.rpm` files are immutable once cached; `repodata/` is served from cache until `cache_ttl_seconds` (default 1 hour) and then revalidated. Upstream metadata and signatures pass through unchanged, so clients must trust the upstream's key. Negative caching and serve-stale: [hosted, proxy, group](../repositories/hosted-proxy-group.md).
