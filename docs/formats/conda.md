<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conda

Conda channel: per-platform `repodata.json`, package download, upload and delete; also as a proxy of an upstream channel (conda-forge) and as a group.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/<platform>/repodata.json` (channel `HOST/repos/REPO`) |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
conda config --add channels HOST/repos/REPO
curl -u <USERNAME>:<TOKEN> -H "X-Filename: my-package-1.0-0.conda" --data-binary @my-package-1.0-0.conda HOST/repos/REPO/linux-64
```

Credentials are Basic with the token as password; see [authentication](../security/authentication.md).

## Endpoints

Relative to `HOST/repos/REPO`; `<platform>` is any path segment such as `linux-64`, `osx-arm64`, `noarch`.

| Method and path | Purpose |
|---|---|
| `GET /<platform>/repodata.json` | index for that platform |
| `GET /<platform>/<filename>` | package download |
| `POST /<platform>` | upload (body = package) |
| `DELETE /<platform>/<filename>` | delete |

## Upload

- Filename comes from the `filename` query parameter or the `X-Filename` header (`400 filename required` if neither).
- The file name must be `<name>-<version>-<build>.tar.bz2` or `<name>-<version>-<build>.conda`. It is split from the right: the build is the part after the last `-`, the version the part before that, and the name everything before, so names containing `-` (`scikit-learn-1.0-py39_0.tar.bz2`) work. Earlier releases split at the first two hyphens and stored such a package as name `scikit`, version `learn`; migration `019` re-splits those rows. Any other extension, fewer than three parts, an empty part, a path separator or a part longer than its column (name 255, version 100, build 255) is `400 invalid filename format`.
- The `.tar.bz2` and the `.conda` of one name, version, build and platform are separate files and can both be uploaded. Uploading a file that already exists is `409 package file already exists` and changes nothing; delete it first to replace it.
- Body limit 500 MiB. sha256 and md5 are computed; success is `201` with `{id, name, version, sha256}`.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced on upload (`507` over quota) and released when the file is deleted for good.
- `info/index.json` is read from the package (from `info-*.tar.zst` inside a `.conda`) for the license policy and for the repodata fields below. Reading is bounded: at most 256 MiB of the decompressed archive and 100000 archive entries are scanned, and a package whose `info/index.json` cannot be found or parsed within that is accepted with empty `depends`, build number `0` and no timestamp.

## repodata.json

`info.subdir` is the platform. `.tar.bz2` files are listed under `packages` and `.conda` files under `packages.conda`, keyed by file name (`<name>-<version>-<build>.tar.bz2` or `.conda`). Each entry has:

| Field | Source |
|---|---|
| `name`, `version`, `build` | the file name |
| `build_number` | `info/index.json` (`0` when absent) |
| `depends` | `info/index.json` (`[]` when absent) |
| `subdir` | the platform the file was uploaded to |
| `sha256`, `md5`, `size` | computed on upload |
| `timestamp` | `info/index.json`, in milliseconds; omitted when the package declares none |
| `constrains`, `license`, `license_family`, `noarch`, `arch`, `platform`, `features`, `track_features` | `info/index.json`, copied as they are when present |

Packages stored before these fields were kept have no `info/index.json` data (empty `depends`, build number `0`, no `md5` or `timestamp`). They are all listed as `.tar.bz2`, including files that were uploaded with a `.conda` name (the extension was not recorded then), until they are deleted and uploaded again.

## Proxy and group repositories

A conda proxy takes a channel URL as its upstream (`https://conda.anaconda.org/conda-forge`; the URL is required, there is no default). Clients use the proxy or group exactly like a hosted channel, with `conda`, `mamba`, `micromamba` or `pixi`:

```sh
conda create -n demo --override-channels -c HOST/repos/REPO tzdata
micromamba create -n demo --override-channels -c HOST/repos/REPO tzdata
pixi init --channel HOST/repos/REPO && pixi add tzdata
```

A repository that is not public needs credentials: put them in the channel URL as `http://USERNAME:TOKEN@HOST/repos/REPO` (Basic authentication with the token as password, see [authentication](../security/authentication.md)); `conda`, `mamba`, `micromamba` and `pixi` all accept it, so mind where the URL is stored (`.condarc`, `pixi.toml`). Proxy and group repositories are read-only: uploads and deletes are refused with `405`.

### What a proxy serves

Only these paths (relative to `HOST/repos/REPO`) are answered; everything else is `404` and clients fall back to what is served:

| Path | Notes |
|---|---|
| `<subdir>/repodata.json`, `.json.zst`, `.json.bz2` | the upstream file, byte for byte (with a minimum age: rebuilt, no `.bz2`, see below) |
| `<subdir>/current_repodata.json`, `.json.zst`, `.json.bz2` | the upstream file, byte for byte (with a minimum age: rebuilt, no `.bz2`, see below) |
| `<subdir>/<name>-<version>-<build>.conda` / `.tar.bz2` | package, verified (below) |
| `channeldata.json` | the upstream file, byte for byte |

`repodata.jlap` (incremental updates) and sharded repodata (`repodata_shards.msgpack.zst`) are not served: `conda`, `mamba` and `pixi` fall back to the whole `repodata.json.zst`, which is also the cheapest file to move (conda-forge `linux-64` is 59 MB compressed against 460 MB plain). Repodata is not rewritten: package URLs in it are relative to the channel, so downloads resolve through the proxy.

- **Caching.** A repodata or channeldata file is cached for the repository's `cache_ttl_seconds` and served from the cache until it expires, then downloaded again whole (there is no conditional revalidation, so set a TTL that fits how fresh the channel has to be; conda clients revalidate against the proxy's `ETag` on their own schedule). When the upstream is down a cached copy is served for up to `max_stale_seconds`. Unlisted names are remembered for `negative_cache_ttl_seconds`.
- **Large indexes.** Index files are streamed to a temporary file while they are downloaded, validated with a streaming JSON scan (no document is ever held in memory), and then served from storage. The limit is 2 GiB, as downloaded and as decompressed (a compression bomb is refused). An index that is not a JSON object with `packages` or `packages.conda`, or that carries `info.base_url` (which would send clients to download from another host), is refused with `502` and not cached.
- **Package verification.** A package is only served if the upstream's `repodata.json.zst` (else `repodata.json`) lists it, and only if the bytes downloaded match the listed `sha256`. An entry without `sha256` is checked against `md5` and `size`. A mismatch is `502` and nothing is cached; a file the repodata does not list is `404`. A package whose entry has no digest at all is cached for the repository TTL only, not as an immutable file. Verified packages are cached immutably.
- **Minimum age.** With `min_age_hours` set on the proxy, a package whose repodata entry has a `timestamp` younger than that is hidden: `repodata.json`, `current_repodata.json` and their `.zst` copies are then rebuilt from the cached upstream file without those entries (a streaming pass spooled to disk, bounded like a group's merge, so each rebuild costs CPU in proportion to the index), and a download of such a package answers `403` unless it is already cached. The rebuilt document has `info.subdir`, `packages` and `packages.conda` only (the same form a group serves; `removed` and other top-level keys are not carried over), and `.json.bz2` answers `404` (it cannot be produced; clients fall back to `.zst` or plain). `timestamp` is the build time the package's author wrote into the package, in milliseconds (older entries in seconds), not the time it was uploaded to the channel; an entry without one is not held back. The `ETag` changes with the threshold and, every 5 minutes, with the clock. A group with the proxy as a member merges the rebuilt files. See [Minimum age](../repositories/hosted-proxy-group.md#minimum-age).
- **Other behavior** that every proxy shares (credentials sent only to the upstream origin, [quarantine and scanning](../security/scanning.md), RBAC, size limits, never exposing upstream error text) is described under [proxy repositories](../repositories/hosted-proxy-group.md).

### What a group serves

A group of conda repositories (hosted and proxy members) serves:

| Path | Behavior |
|---|---|
| `<subdir>/repodata.json`, `.json.zst`, `current_repodata.json`, `.json.zst` | merged across the readable members |
| `<subdir>/<file>` | the first member that has the package, after [hosted wins](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name) |

`repodata.json.bz2`, `repodata.jlap`, sharded repodata and `channeldata.json` are `404` for a group.

- **Merge.** Each member's `packages` and `packages.conda` are united by file name, the earliest member winning a name two members list; the result has `info.subdir` and `repodata_version: 1` (`removed` is dropped). Proxy members are read through their `.zst`, falling back to the plain file; a hosted member renders its `repodata.json`. `current_repodata.json` of a group is the merge of the proxies' `current_repodata.json` and the hosted members' whole `repodata.json`.
- **Hosted wins.** Unless the group has `merge_proxy_versions`, a proxy member's entries are left out of the merged repodata when a hosted member holds a package of the same name (compared case-insensitively, in any platform: an internal `noarch` package hides a public `linux-64` package of that name). The same rule decides package downloads: a proxy's file for a name a hosted member holds is `404`. If a hosted member's names cannot be read, the request fails with `502` rather than serve what should be hidden.
- **Validators.** The group's `ETag` is derived from the validators the members answer for their repodata (a conditional request to each, which a hosted member and a proxy answer without sending the file) and from the set of hosted names, so a revalidation that nothing changed is answered `304` in milliseconds without reading or merging anything. A member that failed, or that gives no validator, makes the response partial: it is served with `Cache-Control: no-store` and no `ETag` (the same holds when a member's repodata turns out unreadable while merging; that member's packages are missing and the others are served). With no member contributing the answer is `404`, or `502` when a member failed.
- **Cost.** The merged document is built on every full request, streamed through temporary files (`TMPDIR`), so disk space of about the size of the plain merged document (about 460 MB for conda-forge `linux-64`) is used for the duration of a request, at most two merges run at a time, and memory stays flat. The `.zst` response is compressed on the fly at the default level, so it is about a quarter larger than the upstream's file. On a laptop with conda-forge as the upstream, a cold `linux-64` merge takes about 5 s and the process peaks at about 175 MB.
