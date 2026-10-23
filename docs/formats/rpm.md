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

`repomd.xml.asc` and `public.key` exist only when a signing key applies to the repository (a managed key from the admin API or `oa gpg`, or `GPG_ENABLED=true` with `GPG_KEY_PATH`; see [Signing](../security/signing.md)). Without signing set `repo_gpgcheck=0` and drop `gpgkey`. For private repositories add `username=` and `password=<TOKEN>` to the repo file (the username is ignored).

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

Set `GPG_ENABLED=true`, `GPG_KEY_PATH` and `GPG_PASSPHRASE` ([signing](../security/signing.md)): the worker then writes `repomd.xml.asc` and `public.key`, which `repo_gpgcheck=1` and `gpgkey=` use. `repomd.xml.asc` is a standard ASCII-armored detached signature (`gpg --verify repomd.xml.asc repomd.xml` succeeds with the key from `public.key`). 

### Signing packages (`sign_packages`)

`repo_gpgcheck=1` only proves the metadata is genuine. dnf and yum default to `gpgcheck=1`, which also wants a signature inside each `.rpm`. Turn on **Sign uploaded packages** in the repository settings, `oa repo update NAME --sign-packages` or `PATCH /api/v1/repository/NAME` with `{"sign_packages": true}` (hosted `rpm` repositories only, off by default). Every upload then gets an OpenPGP header signature from the key that signs the repository's metadata (repository key, then global key, then the configured key file; see [Signing](../security/signing.md)). Clients configure:

```ini
gpgcheck=1
repo_gpgcheck=1
gpgkey=HOST/repos/REPO/public.key
```

How it behaves:

- **The signed file is the stored file.** Signing rewrites the RPM signature header, so the result is a new blob with its own SHA-256 and size. Only that blob is kept; the upload is not stored. The package's size, checksum in `primary.xml`, `repomd.xml` and the API all describe the bytes clients download, so `dnf` checksums match. The package payload and header are untouched; `rpm -K` reports the digests and the signature as OK.
- **Uploading the original again is a duplicate** (`409`, same NEVRA). Keep the unsigned file if you need it elsewhere.
- **No key, no upload.** If `sign_packages` is on and no key applies, the upload fails with `409 SIGNING_KEY_REQUIRED`; nothing is stored unsigned. A package that cannot be signed is `422 SIGNING_FAILED`: the signer needs the payload digest rpm 4.14+ writes and the rpm 4 package format (packages built with `_rpmformat 6` are not supported yet).
- **Existing packages are not re-signed.** Turning the setting on affects later uploads; rotating the key does not touch packages already stored, which stay valid against the key that signed them. Keep the old public key in the clients' keyring (`rpm --import`) or re-upload the packages after deleting them.
- Proxy repositories serve upstream packages with the upstream signature; `sign_packages` is rejected for them.

Debian repositories need nothing equivalent: apt verifies the signed `Release`/`InRelease`, which already covers every package through its checksums.

`primary.xml` lists each package's installed size (`RPMTAG_LONGSIZE`/`SIZE`) and archive size (rpm 4 signature tag 271/1007, or `PAYLOADSIZEALT` for rpm 6 packages), which dnf uses for its transaction size checks. Packages uploaded by earlier versions are re-read from storage the first time their repository's metadata is regenerated, which the upgrade triggers once at startup.

## Proxy

An RPM proxy repository mirrors `repodata/` and `Packages/` from `upstream_url`. `.rpm` files below a `Packages/` directory are immutable once cached; `.rpm` files elsewhere (flat layouts) and `repodata/` are served from cache until `cache_ttl_seconds` (default 1 hour) and then revalidated. Upstream metadata and signatures pass through unchanged, so clients must trust the upstream's key. Negative caching and serve-stale: [hosted, proxy, group](../repositories/hosted-proxy-group.md).
