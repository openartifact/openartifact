<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Hosted, proxy and group repositories

Every repository has a format and a type (`repo_type`). Use this page to create repositories and to understand how they behave.

- `hosted` (default) stores packages that you publish.
- `proxy` mirrors an upstream registry and caches what clients fetch.
- `group` combines hosted and proxy repositories of one format behind a single URL.

Proxy and group repositories use the same client URLs as hosted ones.

## Which section do I need?

- Create a repository: [Creating repositories](#creating-repositories).
- Mirror a public registry: [Proxy repositories](#proxy-repositories).
- Hide freshly published versions: [Minimum age](#minimum-age).
- Block or allow packages: [Package curation](#package-curation).
- Combine repositories behind one URL: [Group repositories](#group-repositories).
- Make a repository readable without a token: [Public repositories and RBAC](#public-repositories-and-rbac).
- Format-specific behaviour (URLs, upload, proxy and group details): the page of your format in the [format matrix](../formats/README.md#format-matrix).

## Type and format support

Every format can be a proxy or a group, and every format except huggingface can be hosted (a hosted huggingface repository is refused with `400 validation_failed`). Proxy and group repositories are read-only: uploads and deletes answer `405`.

A proxy needs an `upstream_url`. These formats have a default, so you may leave it out:

| Format | Default upstream |
|---|---|
| ansible | `https://galaxy.ansible.com` |
| cargo | `https://index.crates.io` |
| composer | `https://repo.packagist.org` |
| conan | `https://center2.conan.io` |
| docker | `https://registry-1.docker.io` |
| go | `https://proxy.golang.org` |
| huggingface | `https://huggingface.co` |
| nix | `https://cache.nixos.org` |
| nuget | `https://api.nuget.org/v3/index.json` |
| pacman | `https://geo.mirror.pkgbuild.com` |
| pypi | `https://pypi.org/simple` |
| rubygems | `https://rubygems.org` |
| terraform | `https://registry.terraform.io` |

All other formats need an explicit `upstream_url`. A conda proxy takes a channel URL such as `https://conda.anaconda.org/conda-forge`.

## Creating repositories

Creating, updating and deleting repositories needs an admin principal (`403 forbidden` otherwise).

Names match `^[a-zA-Z0-9_-]{1,100}$` and must not be one of `new`, `settings`, `trash`, `ivy`, `maven`, `raw`, `sbt` (case-insensitive; `400 validation_failed`).

- `new`, `settings` and `trash` collide with web UI pages under `/repos/`.
- `ivy`, `maven`, `raw` and `sbt` collide with the `/repos/<format>/{name}/...` URL prefixes.
- A repository created earlier under one of these names keeps working through the API and CLI, but some web pages and native URLs may not load. To fix it, create a repository with another name and copy the content (`oa import` or the native client).

```sh
# Hosted (repo_type defaults to "hosted")
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"releases","format":"maven","description":"Release builds","public":false}'

# Proxy
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"maven-central","format":"maven","repo_type":"proxy",
       "upstream_url":"https://repo.maven.apache.org/maven2",
       "upstream_username":"","upstream_password":"",
       "cache_ttl_seconds":3600,"negative_cache_ttl_seconds":60,"max_stale_seconds":604800}'

# Group: ordered members, first has priority
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"maven-all","format":"maven","repo_type":"group",
       "member_repos":["releases","maven-central"]}'
```

| Field | Applies to | Meaning |
|---|---|---|
| `name`, `format`, `description`, `public` | all | `public` grants anonymous read ([see below](#public-repositories-and-rbac)). |
| `repo_type` | all | `hosted` (default), `proxy`, `group`. Any other value: `400`. |
| `upstream_url` | proxy | Plain `http(s)` base URL. Required unless the format has a [default upstream](#type-and-format-support). |
| `upstream_username`, `upstream_password` | proxy | HTTP Basic credentials sent upstream (for docker: sent to the registry's token service). Stored encrypted, never returned. |
| `cache_ttl_seconds` | proxy | Default 3600 (1 h), max 2592000 (30 d). |
| `negative_cache_ttl_seconds` | proxy | Default 60, max 86400 (1 d). |
| `max_stale_seconds` | proxy | Default 604800 (7 d), max 31536000 (365 d), `0` disables serve-stale. |
| `min_age_hours` | proxy | Default `0` (off), max 87600 (10 y). See [Minimum age](#minimum-age). |
| `curation_mode` | proxy | `off` (default), `denylist` or `allowlist`; the [curation rules](#package-curation) take effect only in the last two. Any other value: `400`. |
| `member_repos` | group | Member repository names in priority order, at most 200. |
| `trash_retention_days` | all | Days deleted items stay in the [trash](../operations/trash.md). Integer `>= 0`, or `null`/omitted for the server default (`OPENARTIFACT_TRASH_RETENTION`, 7); `0` deletes permanently and immediately. Negative is `400`. |
| `immutable_releases` | all | Default `false`. When `true`, deleting or overwriting a released version is `409` unless an admin passes `?force=true` ([per format](../operations/trash.md#immutable-releases)). |
| `merge_proxy_versions` | group | Default `false`: for a package name held by a readable hosted member, the group hides what proxy members offer under that name (hosted members win, see [hosted wins](#hosted-members-win-a-package-name)). `true` merges proxy versions with the hosted ones. Global admins only: a delegated project maintainer sending it gets `403`. Ignored/refused on other repository types. |
| `sign_packages` | hosted rpm | Default `false`. When `true`, new RPM uploads are signed with the active GPG key; existing packages are not re-signed. `400` for any other repository. |
| `federated` | hosted | Default `false`. When `true`, the repository replicates with peer instances ([federation](../operations/federation.md)). Needs `federation.enabled` on the server, a federation-capable format and global admin rights; `400` otherwise. Can also be switched on later from the Federation section of the repository's Settings tab or `PUT /api/v1/repositories/{name}/federation`. |

- For `cache_ttl_seconds` and `negative_cache_ttl_seconds`, `0` or an omitted field selects the default (it does not disable caching); negative values are `400`; values above the max are clamped. For `max_stale_seconds` only an omitted field selects the default: an explicit `0` means "never serve stale"; negative is `400`.
- `upstream_*`, TTL fields, `min_age_hours`, `curation_mode` and `member_repos` on a repository of another type are `400 validation_failed`. A name that already exists is `409 conflict`, including the name of a repository that is still in the [trash](../operations/trash.md#repositories) (names stay reserved until the repository is purged or restored).
- The response shows `upstream_url`, `upstream_username`, the three TTL fields, `min_age_hours` and `curation_mode` for proxies only; the password is never included. `upstream_password_set` is `true` when a password is stored, so a client can show that without ever seeing the secret.

Update with `PATCH /api/v1/repository/{repo}` (note the singular `repository`; admin only). Accepted fields: `description`, `public`, `blocked`, `trash_retention_days`, `immutable_releases`, the proxy fields above (`upstream_url` may not be emptied), `merge_proxy_versions` for groups (global admins only), and `member_repos` for groups (replaces the list; `[]` clears it, an absent field leaves it unchanged). Name, format and type cannot change. A blocked repository answers `403` on proxy requests and is skipped as a group member. `GET /api/v1/repository/{repo}` returns one repository; `GET /api/v1/repositories` lists the ones the caller may read.

Every repository type also accepts `presign_downloads` on create and `PATCH`: `true` redirects blob downloads to presigned object-store URLs, `false` always streams, `null` inherits the global `storage.presignDownloads` (in a `PATCH`, omit it to leave it unchanged). It only has an effect on S3 and GCS storage; see [Presigned URLs](../operations/storage.md#presigned-urls).

Group membership can also be managed on its own:

```sh
curl HOST/api/v1/repositories/maven-all/members -H "Authorization: Bearer <TOKEN>"
curl -X PUT HOST/api/v1/repositories/maven-all/members -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"member_repos":["releases","maven-central"]}'
```

Each entry has `id`, `name`, `format`, `repo_type`, `priority`. `DELETE /api/v1/repository/{repo}` needs the repository name as confirmation (`?confirm=<name>` or a JSON body `{"confirm":"<name>"}`; `400 confirmation_required` without it) and returns `409` while a release bundle or promotion still references the repository (see [release bundles](release-bundles.md)). Otherwise the repository moves to the [trash](../operations/trash.md) and is hidden everywhere; when its retention expires (or immediately, with `trash_retention_days` 0) the purge removes the packages and reclaims orphaned blobs.

## Proxy repositories

A proxy repository mirrors an upstream registry. A file is fetched on the first request, stored once, listed under the repository (it appears in the package list and can be downloaded there) and served from the cache afterwards.

- Most formats mirror the upstream path by path.
- Cargo, Composer, Conan, Helm, npm, NuGet, PyPI and others rewrite the metadata so that clients download through OpenArtifact. The format pages describe this.
- Paths with empty segments, `.`, `..`, backslashes, control characters or a trailing `/` answer `404`.
- Requests to the upstream ask for `Accept-Encoding: identity`, so bytes are mirrored unchanged.

Format-specific proxy behaviour is on the format pages: [Ansible](../formats/ansible.md#proxy), [apk](../formats/apk.md#proxy), [Cargo](../formats/cargo.md#proxy), [Composer](../formats/composer.md#proxy), [Conan](../formats/conan.md#proxy), [Conda](../formats/conda.md#proxy), [Debian](../formats/debian.md#proxy), [Docker](../formats/docker.md#proxy), [Go](../formats/go.md#proxy), [Helm](../formats/helm.md#proxy), [Hugging Face](../formats/huggingface.md#caching-and-revision-pinning), [Ivy and sbt](../formats/ivy-sbt.md#proxy), [Maven](../formats/maven.md#proxy), [Nix](../formats/nix.md#proxy), [npm](../formats/npm.md#proxy), [NuGet](../formats/nuget.md#proxy), [pacman](../formats/pacman.md#proxy), [PyPI](../formats/pypi.md#proxy), [raw](../formats/raw.md#proxy), [RPM](../formats/rpm.md#proxy), [RubyGems](../formats/rubygems.md#proxy) and [Terraform](../formats/terraform.md#proxy-repositories).

### Editing proxy settings in the web UI

The repository page of a proxy shows its upstream, cache TTLs, serve-stale window, minimum age and blocked state, and **Edit proxy settings** opens them as a form for a global administrator or a maintainer of the repository's project. The form sends one `PATCH` with only the fields you changed:

- An empty TTL sends the server default (`cache_ttl_seconds` 3600, `negative_cache_ttl_seconds` 60); an empty serve-stale window sends the default of 7 days, while an explicit `0` never serves a stale copy. Durations are shown next to the inputs in readable units.
- The upstream password is write-only and never shown. **Keep** leaves the stored one, **Replace** sets a new one, **Remove** clears it. Changing the upstream host, port, scheme or username removes the stored password on the server, so the form says so and offers **Replace**.
- Only a global administrator can change `blocked`; a maintainer sees the switch disabled.
- Values are checked in the browser against the server ranges, and a validation error the server reports for a field is shown under that field.

A proxy with nothing cached yet shows, instead of an empty list, the client command for the repository and says that the first request fetches from its upstream and caches the result.

### Cache TTLs

A cached file is either immutable or mutable.

- An **immutable** file is fetched once and never revalidated. It is served even when the upstream is down.
- A **mutable** file is served for `cache_ttl_seconds`, then fetched again.
- An upstream `404` or `410` is cached for `negative_cache_ttl_seconds` and drops any cached copy of the file. It is never served stale.

Which files are immutable depends on the format:

| Format | Mutable (cached for `cache_ttl_seconds`) | Immutable once cached |
|---|---|---|
| apk | `APKINDEX.tar.gz` | `.apk` files that match the upstream index |
| cargo | index files | crate files |
| conan | `latest`, revision lists, unpinned file lists | files of a published revision |
| deb | `dists/` metadata; `.deb` and `.udeb` outside `pool/` | `.deb` and `.udeb` below `pool/` |
| go | `@v/list`, `@latest`, branch-query `.info` | `.info`, `.mod`, `.zip` of a canonical version |
| helm | `index.yaml`, `.prov` | chart archives |
| ivy, sbt | `-SNAPSHOT` files and metadata | release files that pass their published checksum |
| maven | `maven-metadata.xml`, `archetype-catalog.xml`, `-SNAPSHOT` files | released artifacts and their checksum and signature files |
| npm | packuments | tarballs |
| nuget | metadata | `.nupkg` and `.nuspec` |
| pypi | simple index and project pages | distribution files and their `.metadata` |
| rpm | `repodata/`; `.rpm` outside `Packages/` | `.rpm` below `Packages/` |
| terraform | discovery, version lists, provider documents, `SHA256SUMS`, module archives | provider zips, checked against the upstream checksums |
| raw | everything | none |

Details and exceptions are on the format pages. Three rules apply to every format:

- A file whose upstream publishes no digest is cached for `cache_ttl_seconds` instead of being immutable. It is fetched and checked again once it expires. Examples: a PyPI file with no sha256 in the index, an npm tarball with neither `dist.integrity` nor `dist.shasum`, a Helm chart without `digest`, a NuGet `.nupkg` without `packageHash`, and a Go file the checksum database does not record.
- Debian, Maven and RPM files are not checked against a digest. They are immutable only where the layout guarantees that a file name is never reused.
- A `200` answer with an HTML `Content-Type` for a binary path (a captive portal or firewall page) is refused with `502` and not cached. This covers Maven `.jar`, `.pom`, `.zip`, `.war`, `.ear`, `.aar`, `.module` and `.gz` files, `.deb`/`.udeb` files and `.rpm` files.

### Upstream failures and serve-stale

Any upstream failure other than 404/410 answers `502` with a generic message and caches nothing, except as described here: 401/403, a timeout, a redirect to a disallowed address, a file over 4 GiB, or 5xx.

When an expired entry cannot be revalidated because the upstream is unreachable (including a host name that does not resolve), times out (30 s for response headers), answers 5xx or breaks the transfer, the cached copy is served with `200` and `Warning: 110 - "Response is Stale"`, and a warning is logged. This holds for up to `max_stale_seconds` past the entry's expiry; with `0` the client gets `502`. A successful refetch replaces the entry. 401/403 from the upstream are not treated as an outage and stay `502`.

The proxy cache worker runs every 5 minutes and deletes entries expired by more than one hour. Cached files are additionally kept for the repository's `max_stale_seconds`, so a larger value trades disk for availability. Blobs no other cache entry references are then deleted.

### Minimum age

`min_age_hours` hides versions the upstream published recently, so a freshly published (possibly malicious) release is not picked up straight away. A version the upstream published less than `min_age_hours` ago has both effects:

- it is dropped from the metadata the proxy renders, and "latest" style pointers move to the newest remaining version; a package whose versions are all too new behaves like a package with no versions;
- its files are refused on download with `403` and the reason in the body.

A version whose publish time the upstream does not state is not hidden. Files already in the cache stay served. A version becomes visible by itself once it is old enough (rendered documents are revalidated at most every few minutes). Groups containing the proxy inherit the filter.

The gate applies to the formats whose upstream metadata dates each version: npm, pypi, composer, helm, nuget, rubygems, go, cargo, conda, ansible, huggingface (by commit date; a branch or tag moves to the newest old-enough commit, see [Hugging Face](../formats/huggingface.md#minimum-age-and-curation)). A conda proxy rebuilds `repodata.json` and `.zst` when the gate is on (`.bz2` answers `404`). Other proxy formats (for example deb, rpm, apk, pacman, maven, docker, nix, terraform, raw) ignore the setting because their upstream metadata dates no version.

### Package curation

Package curation lets an administrator decide which upstream packages and versions a proxy may serve, instead of mirroring everything the upstream publishes. A proxy repository has a `curation_mode` and a list of rules:

| `curation_mode` | Effect |
|---|---|
| `off` (default) | The rules are kept but ignored; the proxy serves everything. |
| `denylist` | A version that matches a `block` rule is unavailable; everything else is served. `allow` rules only matter for the [minimum age](#minimum-age). |
| `allowlist` | Only a version that matches an `allow` rule is available. A package no rule allows is unavailable. |

A rule has an `action` (`block` or `allow`), a `package_pattern`, an optional `version_pattern` (empty matches every version) and an optional `reason` (at most 500 characters), which clients that are refused the version see. Patterns are globs over a name without spaces: `*` matches any run of characters (including none, `/` and `:`), `?` exactly one character, anything else itself; matching is case-sensitive and a hostile pattern cannot make it slow. A package pattern is at most 255 and a version pattern at most 128 characters; a repository holds at most 1000 rules.

Rules are matched against the package name in the form below and against the version exactly as the format shows it to clients:

| Format | Package name | Version |
|---|---|---|
| npm | lower case with its scope: `@scope/name` | as in the packument |
| pypi | PEP 503 normalized: lower case, runs of `-`, `_`, `.` as one `-` | as the file names spell it |
| composer | `vendor/name`, lower case | as the p2 document shows it (`1.2.3`, `v1.2.3`, `dev-main`) |
| helm | chart name (the key under `entries`) | as in the index |
| nuget | package id, lower case | normalized (`1.0` is `1.0.0`) |
| rubygems | gem name | without the platform |
| go | module path as `go.mod` shows it (decoded) | with its `v`: `v1.2.3` |
| cargo | lower case, `_` as `-` | as the index shows it |
| conda | `name` of the repodata entry, lower case | `version` of the entry |
| ansible | `namespace.name`, lower case | as Galaxy shows it |
| huggingface | repository id as on the Hub, lower case: `org/model`, `datasets/org/name`, `spaces/org/name` | the full commit hash |
| maven, ivy, sbt | `group:artifact` | as the version directory shows it |
| docker | image name as written below the repository (`library/alpine`) | tag or digest as requested |
| conan | recipe name, lower case | as the reference shows it |
| deb, rpm, apk, pacman | package name from the file name | `version` as the file name shows it (`<version>-<release>` for rpm, apk and pacman) |

How a rule is applied:

- A `block` rule beats an `allow` rule: a version matching both is unavailable.
- A matching `allow` rule (in `denylist` or `allowlist` mode) also exempts the version from the [minimum age](#minimum-age), so a version an administrator vetted is served at once.
- With `curation_mode` `off` no rule has any effect, so a rule list can be prepared and switched on later.
- A version that is unavailable is dropped from the metadata the proxy renders (the other versions stay, and "latest" style pointers move to the newest one left; a package left without versions behaves like one with no versions, `404`) and its files are refused with `403` and the reason in the body. The refusal also holds for files already in the cache, so a rule added after a file was cached applies at once. Rendered documents change their `ETag` when the mode or the rules change.
- Curation never needs publish dates: nothing is fetched from the upstream just to apply a rule.
- Groups containing the proxy inherit the rules through that member: a group serves what its proxy members serve.
- The rules of a repository are cached for `DB_AUTH_CACHE_TTL_MS` (default 2000 ms): a change made through the same server instance applies at once, one made through another replica within that time. If the rules cannot be read the request fails with `500`; it is never served ungated.
- The rules table is part of the [recovery journal](../operations/disaster-recovery.md), so a rebuilt instance does not serve again what had been blocked.

Coverage differs by format:

| Formats | Metadata | Download |
|---|---|---|
| npm, pypi, composer, helm, nuget, rubygems, go, cargo, conda, ansible | Blocked versions are left out of what the proxy renders | `403`, also for cached files |
| maven, ivy, sbt, deb, rpm, apk, pacman, conan | Served as the upstream has them: a client may still list a blocked package | `403`, also for cached files |
| docker | Tag lists pass through | Manifests: `403 DENIED`; blobs addressed by digest are not gated |
| huggingface | A blocked revision answers `403` | `403` |
| terraform, raw, nix | Not covered: rules have no effect | Not covered |

Each format page says what is gated and how its names are spelled, for example [npm](../formats/npm.md#minimum-age-and-curation) and [PyPI](../formats/pypi.md#minimum-age-and-curation).

Manage curation with the REST API. Setting the mode needs the same rights as any repository update (`PATCH /api/v1/repository/{repo}` is admin only); the rule endpoints need global admin or the maintainer or owner role in the repository's project, and answer `400` for a repository that is not a proxy:

```sh
# mode: off, denylist or allowlist
curl -X PATCH HOST/api/v1/repository/npm-proxy -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"curation_mode":"denylist"}'

# add a rule; 201 with the rule, including its id
curl -X POST HOST/api/v1/repositories/npm-proxy/curation-rules -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"action":"block","package_pattern":"left-pad","version_pattern":"1.*","reason":"malware"}'

curl HOST/api/v1/repositories/npm-proxy/curation-rules -H "Authorization: Bearer <TOKEN>"
curl -X PATCH HOST/api/v1/repositories/npm-proxy/curation-rules/<ID> -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"reason":"confirmed malware"}'
curl -X DELETE HOST/api/v1/repositories/npm-proxy/curation-rules/<ID> -H "Authorization: Bearer <TOKEN>"
```

The list answers `{"rules":[...]}` in creation order; each rule has `id`, `action`, `package_pattern`, `version_pattern`, `reason`, `created_by` and `created_at`. `PATCH` changes any of `action`, `package_pattern`, `version_pattern` and `reason` and leaves absent fields as they were. A rule id that belongs to another repository, or is not a UUID, is `404`; an invalid action or pattern is `400 validation_failed`; the 1001st rule is `409 conflict`. Every change is written to the [audit log](../operations/monitoring.md#audit-log) as `curation.rule_created`, `curation.rule_updated` or `curation.rule_deleted` (a change of the mode is a `repository.updated` event naming the old and new mode). Both settings can also be kept in a [declarative](../integrations/declarative.md) `Repository` manifest as `curation_mode` and `curation_rules`.

To see why a version is or is not served, ask the repository:

```sh
curl 'HOST/api/v1/repositories/npm-proxy/availability?package=left-pad&version=1.3.0' \
  -H "Authorization: Bearer <TOKEN>"
```

```json
{
  "available": false,
  "reasons": [{"source": "curation", "detail": "blocked by repository curation: malware", "rule_id": "..."}],
  "min_age": "off"
}
```

It needs read access to the repository. Each reason has a `source` (`curation`, `min_age`, `quarantine`, `license_policy` or `repository_blocked`) and a `detail`; a `min_age` reason carries `until`, a `curation` reason the `rule_id` that blocks the version (none when an allowlist simply does not list it). For a proxy the answer covers the blocked flag, the rules and the minimum age. The publish time is read from the metadata already cached, never fetched, and reported as `published_at`; `min_age` is `off` (no minimum age), `checked` or `unknown` (the repository has a minimum age but the cached metadata does not date the version, so it could not be checked). For a hosted repository it covers the blocked flag and the quarantine and license-policy holds of the stored version, for the formats stored in the generic package tables (scan results are matched through the version's files). A group has no verdict of its own and answers `400`: ask one of its members.


To tell a version the repository holds back from one that does not exist, list the held-back versions of a package:

```sh
curl 'HOST/api/v1/repositories/npm-proxy/held-back-versions?package=left-pad' \
  -H "Authorization: Bearer <TOKEN>"
```

```json
{
  "supported": true,
  "versions": [
    {"version": "1.3.1", "published_at": "2026-10-31T08:00:00Z",
     "reasons": [{"source": "min_age", "detail": "version is younger than the repository's minimum age", "until": "2026-11-01T08:00:00Z"}]},
    {"version": "1.2.0",
     "reasons": [{"source": "curation", "detail": "blocked by repository curation: malware", "rule_id": "..."}]}
  ]
}
```

The list is newest first and is computed with the same gate as downloads, from the upstream metadata already cached (the upstream is never asked), so it is empty with `"supported": false` when nothing of the package is cached yet or the format cannot list versions with their times. Every format the gate filters can: npm, cargo, composer, helm, rubygems, ansible and conda from their cached metadata; a PyPI proxy only when its cached project page is JSON (an HTML page states no upload times); a NuGet proxy for the pages inline in the cached registration index; a Go proxy from the cached `@v/list`, where a version whose `.info` was never fetched has no time (the minimum age cannot hold it back, curation still can); a conda version is dated by its earliest build. It needs read access to the repository; a repository that is not a proxy is `400`. The package page of the web UI shows these versions greyed out in the Versions tab, with "available in X" or the reason for the block.

To list every version of a package the repository knows of, not only the held ones, with which are already cached:

```sh
curl 'HOST/api/v1/repositories/npm-proxy/proxy-versions?package=left-pad' \
  -H "Authorization: Bearer <TOKEN>"
```

```json
{
  "supported": true,
  "cache_known": true,
  "versions": [
    {"version": "1.3.1", "status": "held", "published_at": "2026-10-31T08:00:00Z", "cached": false,
     "reasons": [{"source": "min_age", "detail": "version is younger than the repository's minimum age", "until": "2026-11-01T08:00:00Z"}]},
    {"version": "1.3.0", "status": "available", "published_at": "2026-09-02T10:00:00Z", "cached": true, "reasons": []},
    {"version": "1.2.0", "status": "available", "published_at": "2026-05-12T09:30:00Z", "cached": false, "reasons": []}
  ]
}
```

Like the held-back list it reads the cache only (the upstream is never asked), is newest first and has the same `supported` rule and format coverage. `status` is `available` (clients can install it) or `held` (the gate keeps it from them; `reasons` says why, empty for an available version). `cached` is `true` when a file of the version is already stored in the proxy cache, so installing it needs no download from the upstream; it is read from the cache keys, which name the version for npm, PyPI, cargo, Go, Helm, NuGet and RubyGems. For the other formats `cache_known` is `false` and `cached` is `false` for every version. The package page of a proxy shows this list with Available, Cached and Held badges and an install command for each version that can be installed.

### Behavior notes

- Concurrent requests for the same uncached path share one upstream fetch. Clients receive the first byte once the whole file is cached.
- Cache hits do not update `hit_count` or `last_hit_at`: that would add a database write to every download.
- Quarantined files answer `403` (`ARTIFACT_QUARANTINED`) like hosted ones.

### Upstream URL and credentials

`upstream_url` is a plain `http(s)` base URL without credentials, query or fragment. Put credentials in `upstream_username` and `upstream_password`.

- OpenArtifact refuses upstreams on loopback, private, link-local and reserved addresses. This is checked when you save the repository and on every fetch and redirect.
- Set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` only for upstreams on your internal network. The cloud metadata range (169.254.x) stays blocked.
- The `Authorization` header is dropped on any redirect to another scheme, host or port, so credentials never follow a redirect.
- Changing the scheme, host, port or `upstream_username` in a `PATCH` clears the stored `upstream_password`, unless the same request sends a new one. A maintainer therefore cannot send a stored credential to a host they control.
- The number of parallel upstream fetches is bounded. Further requests wait for a free slot.

See [`../../.env.example`](../../.env.example) for the settings.

## Group repositories

A group is an ordered list of hosted and proxy members of the same format. Groups cannot be nested, cannot contain themselves or duplicates, and hold at most 200 members; violations are `400 validation_failed`.

- The first member that has the requested file answers. A member answering 404 is passed over, as is one failing with 5xx; when no member has the file the group answers `404`, or `502` if a member failed (so a client does not cache "does not exist" for a file a broken member might have).
- Each member is authorized for the caller as if requested directly (action `read`). A member the caller cannot read behaves like one without the file, so a public group never exposes a private member's files.
- Deb, RPM and pacman groups generate their own, merged metadata (see [Deb, RPM and pacman groups](#deb-rpm-and-pacman-groups)), and Alpine groups a merged, re-signed `APKINDEX.tar.gz` per directory (see [Alpine groups](#alpine-apk-groups)); package files are served by the member the merged index lists them for.
- Every merged answer (cargo index files, Composer p2 documents, Conan revision lists and searches, go `@v/list`, Helm `index.yaml`, npm packuments, NuGet version lists and registrations, pypi pages, RubyGems info and versions files) that left out a failed member is incomplete: it is served with `Cache-Control: no-store` and without an `ETag`, so clients and caches do not keep it. A complete merge carries an `ETag` and revalidates.
### Hosted members win a package name

<a id="hosted-members-win-a-package-name"></a>

This setting (`merge_proxy_versions`, default `false`) protects against dependency confusion.

- If a readable hosted member holds a package name, a public package of the same name on a proxy member adds no versions, metadata entries or files under that name.
- Proxy members still serve names that no hosted member holds.
- The rule applies per package name, not per version. Once a hosted member holds any version of `acme-lib`, no proxy version of `acme-lib` is visible, even a higher one.
- A hosted member the caller cannot read, or that is blocked, counts as absent.
- Set `merge_proxy_versions` to `true` to merge the versions of all members again. This brings back the dependency-confusion risk.

Each format page says what "package name" means for it. Formats whose group answers first-hit, such as raw, do not merge versions and ignore this setting.

### Group URLs

A group uses the same client URL as a hosted repository of its format. Replace the repository name with the group name; see the [format matrix](../formats/README.md#format-matrix).

### Alpine (apk) groups

An apk group publishes an `APKINDEX.tar.gz` per `<branch>/<repo>/<arch>` directory, merged from its members and signed with the group's own key (`HOST/repos/GROUP/public.key`, to be installed into `/etc/apk/keys`). A hosted member's packages win a name over a proxy member's unless `merge_proxy_versions` is `true`; downloads come from the member whose entry the index lists. The rules, the regeneration and what is not served: [Alpine group repositories](../formats/apk.md#group).

### Deb, RPM and pacman groups

Debian, RPM and pacman clients read an index, so a group cannot forward the request to one member: it publishes an index of its own, generated from the members and stored like any other generated metadata.

- **Merging.** The group's `Release`/`InRelease`/`Release.gpg` and `Packages.gz`/`Packages.xz` (Debian), or `repomd.xml`/`repomd.xml.asc` with `primary`, `filelists`, `other` and `modules` (RPM), list every package of every member. Hosted members contribute what is in their database, proxy members what their upstream publishes (read through the proxy cache). Member order decides: a package version (Debian: name, version and architecture; RPM: name, epoch, version, release and architecture) offered by several members is listed once, from the earliest member. The same hosted-wins rule as the other formats applies unless `merge_proxy_versions` is `true`: a package name held by a hosted member is not offered from proxy members.
- **Downloads** come from the member the index lists the file for, so the file a client downloads is always the one whose checksum the index carries. A request for a file the index does not list is `404`, even if some member has it.
- **Proxies over a tree of repositories (RPM).** An RPM proxy may mirror a directory that holds several repositories, each in a sub-directory with its own `repodata/` (`9/BaseOS/x86_64/os/`, `9/AppStream/x86_64/os/`, …) and none at the top. Such a proxy has nothing at its root for the group to merge, so the group serves every path the merged index does not list from the first readable proxy member of that kind that has it, as the proxy itself would: `baseurl=HOST/repos/GROUP/9/BaseOS/x86_64/os/` works, and the `repodata/` and packages under it are the proxy's, unmerged and unsigned by the group (upstream signatures pass through). Proxies that do have a root `repodata/repomd.xml` are covered by the merged index, so their unlisted files stay `404`. A group whose proxy members all lack a root `repodata/repomd.xml` and that has no other package to list has no merged index: `GET /repos/GROUP/repodata/repomd.xml` answers `404` with a body naming the members and pointing at the sub-path form, instead of publishing an empty index. It is generated as soon as a hosted member has a package. Access control is unchanged: sub-paths are only served from members the caller can read.
- **Signing.** The group's metadata is signed with the same server-wide key as hosted repositories ([signing](../security/signing.md)); `HOST/repos/GROUP/public.key` serves its public key. Upstream signatures are not passed through, because the merged index is a new document. Without a key the group is unsigned, exactly like a hosted repository.
- **Access control.** A caller is served an index that lists only the members they can read, and downloads are authorized against the owning member. Callers who can read every member share one index, generated in the background as above. A caller who can read only some members gets an index (with its own signature) generated for exactly that set of members on their first request, which waits for the generation like the first request of a group does; these per-set indexes are kept until the group is regenerated, which drops them, so the next such request generates it again. A member the caller cannot read behaves as if it were not in the group, including for the hosted-wins rule. A caller who can read no member gets `404`. The signing key and `public.key` are the same for all callers.
- **Regeneration.** The index is regenerated in the background when a hosted member changes (upload, delete, trash, restore, cleanup), when the group's members or settings change, when a member is deleted or blocked, and at startup for groups that have no index yet. Proxy members have no change signal, so a request that finds the index older than the shortest `cache_ttl_seconds` among them (at least one minute) serves it and refreshes it in the background. A proxy member that cannot be reached is left out of that run, the run is marked partial and retried after a minute; an upstream that simply does not have a distribution is a complete answer. The first request for a group that has no index yet waits for the generation (at most two minutes, then `503` with `Retry-After`), which for a group of wide upstreams (a full distribution mirror) downloads their indexes once; later runs reuse the proxy cache.
- **Debian distributions.** A group publishes the distributions that were requested and exist in at least one member (the hosted members' distributions and the proxies' upstream releases); asking for one that no member has is `404`. The component/architecture indexes follow the union of what the members declare.
- **pacman groups** have the layout of a hosted repository: `Server = HOST/repos/GROUP/os/$arch`, with the group's name as the database name (`GROUP.db`, `GROUP.files`), and `HOST/repos/GROUP/public.key` for the key. For every architecture directory the group serves, it publishes the `.db` and `.files` databases (and their `.sig`) merged from its members' databases for that architecture. Hosted members contribute their packages (an `any` package is listed in every architecture); a proxy member's upstream must be a **section** URL such as `https://geo.mirror.pkgbuild.com/extra`, whose `os/<arch>/extra.db` is read through the proxy cache (a proxy whose URL has no path cannot supply a database and is skipped, with a log line; a proxy of a whole mirror root still works as a standalone proxy). A pacman database lists one version of a package name, so the group lists each name once: the first member in order that has it, except that a name held by a hosted member is never taken from a proxy member, whatever their order, unless `merge_proxy_versions` is `true`, in which case the newest version (by pacman's version comparison; a tie goes to the earlier member) wins. Package files and their `.sig` come from the member that owns the listed file name, like deb and RPM downloads. Architectures are generated on the first request for them (`os/<arch>/GROUP.db`) when some member has packages for them (`404` otherwise) and then kept up to date with the group. The `.files` database lists files for hosted members; for proxy members it is read best-effort from the upstream's `<section>.files` (capped in size) and packages without a list are listed without files. Package signatures are not re-made: each database entry carries its package's own signature (the server's for a hosted member, the upstream's for a proxy member), so with `SigLevel = Required` the client must trust the key of every member it takes packages from: hosted repositories sign with the server-wide key unless they have their own, which is also the group's key by default; upstream-signed packages additionally need the upstream's keyring (`archlinux-keyring` for Arch mirrors). See [pacman](../formats/pacman.md#group).
- **Not merged or served by a group:** Debian `by-hash` (the plain `Packages` file is served too, decompressed from the stored `Packages.gz`, because `Release` lists it as apt requires), `Contents`, `Sources` and translations; RPM `comps.xml`, `updateinfo` and the sqlite databases, and `xml:base` redirects of upstream packages (dropped, so clients cannot be sent to another host). Modules of RPM members are merged (the first member wins a module/stream/version/context, and so do module defaults per module).

## Public repositories and RBAC

`public: true` grants anonymous `read` (never write) on that repository, independent of the global `allowAnonymousRead` setting. Everything else is decided by RBAC: `/repos/{repo}/...` maps to read/write by HTTP method and `/api/v1/*` to `manage`, against the caller's permission targets. Group members are checked individually, as above. Details: [RBAC](../security/rbac.md). Package clients authenticate with an API token (Bearer, or Basic with the token as password); account passwords only work for the web UI login.
