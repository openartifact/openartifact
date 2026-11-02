<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conda

Use a conda repository as a channel for `conda`, `mamba`, `micromamba` and `pixi`. Host your own packages, mirror an upstream channel such as conda-forge with a proxy, or combine both in a group.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO` (index at `HOST/repos/REPO/<platform>/repodata.json`) |
| Format id | `conda` |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |
| Default upstream (proxy) | none; `upstream_url` is required, for example `https://conda.anaconda.org/conda-forge` |

## Client setup

Add the channel and upload with a token. The web UI connect panel shows the same commands with your own URL.

```sh
conda config --add channels HOST/repos/REPO
curl -u <USERNAME>:<TOKEN> -H "X-Filename: my-package-1.0-0.conda" --data-binary @my-package-1.0-0.conda HOST/repos/REPO/linux-64
```

Credentials are Basic with the token as password; see [authentication](../security/authentication.md).

A private repository needs the credentials in the channel URL:

```sh
conda create -n demo --override-channels -c http://<USERNAME>:<TOKEN>@HOST/repos/REPO tzdata
```

`conda`, `mamba`, `micromamba` and `pixi` all accept this form. The URL ends up in `.condarc` or `pixi.toml`, so protect those files.

## Publish

<a id="upload"></a>

Upload a package to a hosted repository with `POST /<platform>`, where `<platform>` is a path segment such as `linux-64`, `osx-arm64` or `noarch`.

```sh
curl -u <USERNAME>:<TOKEN> -H "X-Filename: scikit-learn-1.0-py39_0.tar.bz2" \
  --data-binary @scikit-learn-1.0-py39_0.tar.bz2 HOST/repos/REPO/linux-64
```

- Send the file name in the `X-Filename` header or the `filename` query parameter. Without either the answer is `400 filename required`.
- The file name must be `<name>-<version>-<build>.tar.bz2` or `<name>-<version>-<build>.conda`.
- The name is split from the right: build after the last `-`, version before it, name the rest. Names with hyphens such as `scikit-learn` work.
- Any other extension, fewer than three parts, an empty part or a path separator is `400 invalid filename format`. So is a part longer than its limit: name 255, version 100, build 255.
- The `.tar.bz2` and the `.conda` of one name, version, build and platform are separate files. You can upload both.
- Uploading a file that exists is `409 package file already exists` and changes nothing. Delete it first to replace it.
- The body limit is 500 MiB. Success is `201` with `{id, name, version, sha256}`.
- Over [quota](../repositories/cleanup-and-quotas.md#what-counts) is `507`.

Delete a file with `DELETE /<platform>/<filename>`.

The server reads `info/index.json` from the package (from the `info-*.tar.zst` member of a `.conda`) for the license policy and the repodata fields. If it cannot find or parse that file within the [limits](#limits-and-behaviour), the package is still accepted with empty `depends`, build number `0` and no timestamp.

## Install and consume

Clients use the channel like any other:

```sh
conda create -n demo --override-channels -c HOST/repos/REPO tzdata
micromamba create -n demo --override-channels -c HOST/repos/REPO tzdata
pixi init --channel HOST/repos/REPO && pixi add tzdata
```

Paths relative to `HOST/repos/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /<platform>/repodata.json` | index of that platform |
| `GET /<platform>/<filename>` | package download |

<a id="repodatajson"></a>

### repodata.json

`info.subdir` is the platform. `.tar.bz2` files are listed under `packages` and `.conda` files under `packages.conda`, keyed by file name. Each entry has:

| Field | Source |
|---|---|
| `name`, `version`, `build` | the file name |
| `build_number` | `info/index.json` (`0` when absent) |
| `depends` | `info/index.json` (`[]` when absent) |
| `subdir` | the platform the file was uploaded to |
| `sha256`, `md5`, `size` | computed on upload |
| `timestamp` | `info/index.json`, in milliseconds; omitted when the package declares none |
| `constrains`, `license`, `license_family`, `noarch`, `arch`, `platform`, `features`, `track_features` | `info/index.json`, copied as they are when present |

Packages stored before these fields were kept are listed with empty `depends` and no `md5` or `timestamp`. Delete and upload them again to refresh. See [upgrading](../getting-started/upgrading.md).

## Proxy

<a id="proxy-and-group-repositories"></a>

Create a proxy with `repo_type: "proxy"` and an `upstream_url` that is a channel URL. There is no default. Clients use it exactly like a hosted channel. Proxy and group repositories are read-only: uploads and deletes answer `405`.

### What a proxy serves

Only these paths are answered. Everything else is `404`, and clients fall back to what is served.

| Path | Notes |
|---|---|
| `<subdir>/repodata.json`, `.json.zst`, `.json.bz2` | the upstream file, byte for byte |
| `<subdir>/current_repodata.json`, `.json.zst`, `.json.bz2` | the upstream file, byte for byte |
| `<subdir>/<name>-<version>-<build>.conda` / `.tar.bz2` | package, verified |
| `channeldata.json` | the upstream file, byte for byte |

With a minimum age or curation on, repodata is rebuilt instead and `.json.bz2` answers `404` (see below).

Incremental updates (`repodata.jlap`) and sharded repodata (`repodata_shards.msgpack.zst`) are not served. `conda`, `mamba` and `pixi` fall back to the whole `repodata.json.zst`, which is also the smallest file to move. Package URLs in repodata are relative to the channel, so downloads go through the proxy.

- **Caching.** A repodata or channeldata file is cached for the repository's `cache_ttl_seconds`, then downloaded again whole. There is no conditional revalidation, so choose a TTL that fits how fresh the channel must be. Conda clients revalidate against the proxy's `ETag` on their own schedule.
- **Upstream down.** A cached copy is served for up to `max_stale_seconds`. Unlisted names are remembered for `negative_cache_ttl_seconds`.
- **Large indexes.** Index files are checked while they download and stored on disk, never held whole in memory. The limit is 2 GiB, both as downloaded and as decompressed.
- **Refused indexes.** An index that is not a JSON object with `packages` or `packages.conda` is refused with `502` and not cached. So is an index with `info.base_url`, which would send clients to another host.
- **Package verification.** A package is served only if the upstream `repodata.json.zst` (else `repodata.json`) lists it and the downloaded bytes match the listed `sha256`. An entry without `sha256` is checked against `md5` and `size`.
- **Mismatch.** A mismatch is `502` and nothing is cached. A file the repodata does not list is `404`.
- **Cache lifetime.** Verified packages are cached as immutable. A package whose entry has no digest at all is cached for the repository TTL only.

Credentials, quarantine and scanning, RBAC and size limits work as for every proxy; see [proxy repositories](../repositories/hosted-proxy-group.md#proxy-repositories) and [scanning](../security/scanning.md).

### Minimum age and curation

Both apply to conda. The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The conda specifics:

- **Minimum age.** A package whose repodata entry has a `timestamp` younger than `min_age_hours` is hidden. An entry without a timestamp is not hidden.
- **Curation.** Rules match the `name` of the repodata entry (lower-case, as conda writes it) and its `version`. `channeldata.json` is served as the upstream has it.
- **Index.** `repodata.json`, `current_repodata.json` and their `.zst` copies are rebuilt without the hidden entries. Each rebuild costs CPU in proportion to the index size.
- **Rebuilt form.** The rebuilt document has `info.subdir`, `packages` and `packages.conda` only. `removed` and other top-level keys are dropped.
- **`.bz2`.** `.json.bz2` answers `404` while a minimum age or curation is on. Clients fall back to `.zst` or plain.
- **Download.** A download of a hidden package answers `403`. Under minimum age it is allowed if already cached. Under curation it is refused even when cached.

## Group

A group of conda repositories (hosted and proxy members) is read-only. Use it like a hosted channel.

| Path | Behaviour |
|---|---|
| `<subdir>/repodata.json`, `.json.zst`, `current_repodata.json`, `.json.zst` | merged across the readable members |
| `<subdir>/<file>` | first member that has the package, after [hosted wins](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name) |

`repodata.json.bz2`, `repodata.jlap`, sharded repodata and `channeldata.json` are `404` for a group.

- **Merge.** Each member's `packages` and `packages.conda` are united by file name. The earliest member wins a name that two members list.
- **Merged form.** The result has `info.subdir` and `repodata_version: 1`. `removed` is dropped.
- **Sources.** Proxy members are read through their `.zst`, falling back to the plain file. A hosted member renders its own `repodata.json`. A group's `current_repodata.json` merges the proxies' `current_repodata.json` with the hosted members' whole `repodata.json`.
- **Hosted wins.** Unless the group has `merge_proxy_versions`, a proxy member's entries are left out when a hosted member holds a package of the same name. The name is compared case-insensitively across all platforms.
- **Example.** An internal `noarch` package hides a public `linux-64` package of that name.
- **Downloads.** The same rule applies to downloads: a proxy's file for a name a hosted member holds is `404`.
- **Failure.** If a hosted member's names cannot be read, the request fails with `502` rather than serve what should be hidden.
- **Validators.** The group's `ETag` comes from what the members report for their repodata and from the set of hosted names. A revalidation with no change is answered `304` without reading or merging anything.
- **Partial answers.** A member that failed, or gives no validator, makes the answer partial. It is served with `Cache-Control: no-store` and no `ETag`. With no member contributing the answer is `404`, or `502` when a member failed.
- **Cost.** The merged document is built on every full request. It uses temporary disk space under `TMPDIR` of about the size of the plain merged document, and at most two merges run at a time. The `.zst` response is compressed on the fly at the default level, so it is larger than the upstream's file.

## Limits and behaviour

- **Upload size.** 500 MiB per package.
- **`info/index.json` reading.** At most 1 MiB of `info/index.json`, 256 MiB of decompressed archive and 100000 archive entries are read. For a `.conda` the zstd window is capped at 64 MiB.
- **Index size.** A proxy index may be up to 2 GiB, as downloaded and as decompressed.
- **Quota.** Enforced on upload (`507`) and released when the file is deleted for good.
- **Status codes.** `400` invalid name or missing filename, `409` file exists, `405` write to proxy or group, `403` hidden or curated package, `502` verification mismatch or refused upstream index.
- **Caching.** Repodata and packages carry an `ETag`. Cache-Control is `public, max-age=N` for anonymous reads of a public repository and `private, no-cache` otherwise.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `400 filename required` on upload | No `X-Filename` header or `filename` parameter. Add one. |
| `400 invalid filename format` | The name is not `<name>-<version>-<build>.tar.bz2` or `.conda`, or a part is too long. Rename the file. |
| `409 package file already exists` | The same file is stored. Delete it first to replace it. |
| `405` on upload | The repository is a proxy or group. Upload to a hosted repository. |
| Package shows empty `depends` or build `0` | `info/index.json` was missing, unreadable or over the limits. Rebuild the package and upload again. |
| Download is `502` from a proxy | The bytes did not match the upstream's listed digest, or the upstream index was refused. Retry later; check the upstream. |
| Download is `404` from a proxy | The upstream repodata does not list the file, or `.bz2` or `.jlap` was requested. Use `.zst` or plain. |
| Download is `403` from a proxy | The package is younger than `min_age_hours` or blocked by curation. |
| A group lacks a public package | A hosted member holds the same name. Set `merge_proxy_versions` to `true` to merge, with the dependency-confusion risk. |
