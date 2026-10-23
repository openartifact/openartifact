<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Hosted, proxy and group repositories

Every repository has a format and a type (`repo_type`): `hosted` (default), `proxy` or `group`. Proxy and group repositories are served on the same client URLs as hosted ones.

## Type and format support

11 formats can be proxied and 9 can be grouped (every proxy format except deb and rpm).

| Format | Hosted | Proxy | Group |
|---|---|---|---|
| maven | yes | yes | yes |
| npm | yes | yes | yes |
| raw | yes | yes | yes |
| pypi | yes | yes | yes |
| nuget | yes | yes | yes |
| deb | yes | yes | yes |
| rpm | yes | yes | yes |
| docker | yes | yes | yes |
| cargo | yes | yes | yes |
| go | yes | yes | yes |
| helm | yes | yes | yes |
| rubygems, conda, conan, ivy, sbt | yes | no | no |

Creating a proxy or group repository of an unsupported format is rejected with `400 validation_failed`. Proxy and group repositories are read-only: writes (client uploads and the `/api/v1/repositories/{repo}/{debs,packages}` upload endpoints) answer `405` with `Allow: GET, HEAD`. Client URLs per format are on the [formats](../formats/README.md) pages.

## Creating repositories

Creating, updating and deleting repositories needs an admin principal (`403 forbidden` otherwise). Names match `^[a-zA-Z0-9_-]{1,100}$` and must not be one of the reserved names `new`, `settings`, `trash`, `raw`, `maven`, `sbt`, `ivy` (case-insensitive; `400 validation_failed`): the first three collide with web UI pages under `/repos/`, the last four with the isolated `/repos/<format>/{name}/...` URL namespaces, which would make the repository's pages and native URLs unreachable. Repositories created by an earlier version under one of these names keep working through the API and CLI, but their web pages and some native URLs may not load; there is no automatic migration, so create a new repository with another name and copy the content (`oa import` or the native client) before deleting the old one; `format` is one of the 16 formats above.

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
| `upstream_url` | proxy (required, except docker, pypi, cargo, go and nuget) | Plain `http(s)` base URL. Optional for docker (defaults to Docker Hub, `https://registry-1.docker.io`), pypi (defaults to `https://pypi.org/simple`), cargo (defaults to `https://index.crates.io`), go (defaults to `https://proxy.golang.org`) and nuget (defaults to the nuget.org service index, `https://api.nuget.org/v3/index.json`). |
| `upstream_username`, `upstream_password` | proxy | HTTP Basic credentials sent upstream (for docker: sent to the registry's token service). Stored encrypted, never returned. |
| `cache_ttl_seconds` | proxy | Default 3600 (1 h), max 2592000 (30 d). |
| `negative_cache_ttl_seconds` | proxy | Default 60, max 86400 (1 d). |
| `max_stale_seconds` | proxy | Default 604800 (7 d), max 31536000 (365 d), `0` disables serve-stale. |
| `member_repos` | group | Member repository names in priority order, at most 200. |
| `trash_retention_days` | all | Days deleted items stay in the [trash](../operations/trash.md). Integer `>= 0`, or `null`/omitted for the server default (`OPENARTIFACT_TRASH_RETENTION`, 7); `0` deletes permanently and immediately. Negative is `400`. |
| `immutable_releases` | all | Default `false`. When `true`, deleting or overwriting a released version is `409` unless an admin passes `?force=true` ([per format](../operations/trash.md#immutable-releases)). |
| `merge_proxy_versions` | group | Default `false`: for a package name held by a readable hosted member, the group hides what proxy members offer under that name (hosted members win, see [hosted wins](#hosted-members-win-a-package-name)). `true` merges proxy versions with the hosted ones. Global admins only: a delegated project maintainer sending it gets `403`. Ignored/refused on other repository types. |
| `sign_packages` | hosted rpm | Default `false`. When `true`, new RPM uploads are signed with the active GPG key; existing packages are not re-signed. `400` for any other repository. |
| `federated` | hosted | Default `false`. When `true`, the repository replicates with peer instances ([federation](../operations/federation.md)). Needs `federation.enabled` on the server, a federation-capable format and global admin rights; `400` otherwise. Can also be switched on later from the repository's Federation tab or `PUT /api/v1/repositories/{name}/federation`. |

- For `cache_ttl_seconds` and `negative_cache_ttl_seconds`, `0` or an omitted field selects the default (it does not disable caching); negative values are `400`; values above the max are clamped. For `max_stale_seconds` only an omitted field selects the default: an explicit `0` means "never serve stale"; negative is `400`.
- `upstream_*`, TTL fields and `member_repos` on a repository of another type are `400 validation_failed`. A name that already exists is `409 conflict`, including the name of a repository that is still in the [trash](../operations/trash.md#repositories) (names stay reserved until the repository is purged or restored).
- The response shows `upstream_url`, `upstream_username` and the three TTL fields for proxies only; the password is never included.

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

Requests are mirrored by path from `upstream_url` (npm, cargo and Helm proxies are the exception, see [npm proxies](#npm-proxies), [cargo proxies](#cargo-proxies) and [Helm proxies](#helm-proxies)). A file is fetched on first request, stored content-addressed in the blob store, recorded under the repository (it appears in the package list and can be downloaded there) and served from the cache afterwards. Path segments that are empty, `.`, `..`, contain backslashes or control characters, and directory paths (trailing `/`) answer `404`; upstream requests carry `Accept-Encoding: identity`, so bytes are mirrored unchanged.

### Cache TTLs

- `cache_ttl_seconds` bounds how long mutable files are served without refetching: Maven `maven-metadata.xml`, `archetype-catalog.xml` and `-SNAPSHOT` files, Debian `dists/` and RPM `repodata/` metadata, PyPI simple index and project pages, Helm `index.yaml` and `.prov` files, Go `@v/list`, `@latest` and branch-query `.info` files, and everything in raw repositories.
- Released Maven artifacts (and their `.md5`/`.sha1`/`.sha256`/`.sha512`/`.asc` files; not `maven-metadata*.xml`, `archetype-catalog.xml` or `-SNAPSHOT` files), `.deb`/`.udeb` files below a `pool/` directory, `.rpm` files below a `Packages/` directory and PyPI distribution files (and their `.metadata`), npm tarballs, cargo crate files, Helm chart archives, Go `.info`/`.mod`/`.zip` of a canonical version and NuGet `.nupkg`/`.nuspec` files are immutable once cached and never revalidated.
- Maven, Debian and RPM files are not checked against a digest, so they are pinned only where the layout guarantees a file name is never reused. `.deb`/`.udeb` files outside `pool/` (flat repositories) and `.rpm` files outside a `Packages/` directory are cached with `cache_ttl_seconds` instead. A `200` answer with an HTML `Content-Type` for a Maven `.jar`/`.pom`/`.zip`/`.war`/`.ear`/`.aar`/`.module`/`.gz`, `.deb`/`.udeb` or `.rpm` path (a captive portal or WAF page) is refused with `502` and not cached.
- A file the upstream publishes no digest for is cached with `cache_ttl_seconds` instead, so it is fetched and checked again once it expires: a PyPI file with no sha256 in the index, an npm tarball with neither `dist.integrity` nor `dist.shasum`, a Helm chart without `digest`, a NuGet `.nupkg` without a `packageHash`, and a Go `.mod`/`.zip` of a module version the checksum database has no record of. (With the checksum database switched off on the server, Go files stay immutable.)
- An upstream 404/410 is cached for `negative_cache_ttl_seconds` and drops any cached copy of the file (it is never served stale).

### Upstream failures and serve-stale

Any upstream failure other than 404/410 answers `502` with a generic message and caches nothing, except as described here: 401/403, a timeout, a redirect to a disallowed address, a file over 4 GiB, or 5xx.

When an expired entry cannot be revalidated because the upstream is unreachable (including a host name that does not resolve), times out (30 s for response headers), answers 5xx or breaks the transfer, the cached copy is served with `200` and `Warning: 110 - "Response is Stale"`, and a warning is logged. This holds for up to `max_stale_seconds` past the entry's expiry; with `0` the client gets `502`. A successful refetch replaces the entry. 401/403 from the upstream are not treated as an outage and stay `502`.

The proxy cache worker runs every 5 minutes and deletes entries expired by more than one hour. Cached files are additionally kept for the repository's `max_stale_seconds`, so a larger value trades disk for availability. Blobs no other cache entry references are then deleted.

### Behavior notes

- Concurrent requests for the same uncached path share one upstream fetch. Clients get the first byte once the whole file is cached.
- Cache hits do not update `hit_count` or `last_hit_at`: that would add a database write to every download.
- Quarantined files answer `403` (`ARTIFACT_QUARANTINED`) like hosted ones.

### PyPI proxies

A pypi proxy is not a path mirror. `upstream_url` is the simple index (default `https://pypi.org/simple`); clients use `pip install --index-url HOST/repos/PROXY/simple/ <package>`.

- `GET /simple/` and `GET /simple/<project>/` are fetched from the upstream (PEP 691 JSON preferred, HTML accepted), cached under `cache_ttl_seconds`, and served stale when the upstream is down (see above). Names are normalized per PEP 503 before the upstream is asked; names that are not plain `[a-z0-9-]` are `404`.
- The client gets HTML or PEP 691 JSON by `Accept` negotiation (HTML when no preference, `406` when neither is acceptable), independent of what the upstream spoke. The page cache holds the upstream document; pages are re-rendered per request, so `Vary: Accept` and a per-serialization `ETag` apply.
- Every file link is rewritten to `/repos/PROXY/files/<project>/<filename>`. `#sha256=…`, `data-requires-python`, `data-core-metadata` / `data-dist-info-metadata` (and the JSON equivalents), yanked state, size and upload time are preserved; the real upstream URL is never exposed and never taken from the request. The OpenArtifact download resolves the filename in the cached project page, so only files the upstream lists can be fetched. Files with unsafe names are dropped from the page. GPG signatures (`.asc`) are not mirrored.
- A distribution is fetched on first download, hashed while spooling and compared with the sha256 the index advertises (and `.metadata` with the metadata hash); a mismatch is `502`, nothing is stored. A file with no sha256 in the index is cached unverified, for the repository TTL only. Verified files are immutable, so a second install works with the upstream down.
- The upstream's Basic credentials go only to the upstream's own origin: files hosted on another origin (for example `files.pythonhosted.org`) are fetched without them.
- Uploads (`twine upload` to `/legacy/`) are `405`.

### npm proxies

An npm proxy (`upstream_url` such as `https://registry.npmjs.org`) is a registry, not a path mirror: clients use `HOST/repos/REPO/` as their registry exactly as with a hosted npm repository ([client setup](../formats/npm.md#client-setup)).

- **Packuments** (`GET /<pkg>`, `GET /@scope%2fname`, also `@scope/name`) are fetched from the upstream, cached as received with the repository TTL (`cache_ttl_seconds`) and served stale on upstream failure like every other proxy file. A client sending `Accept: application/vnd.npm.install-v1+json` (npm, pnpm and yarn do) gets the abbreviated document; it and the full document are cached separately. Each response is rewritten: every `dist.tarball` points at `BASE_URL/repos/REPO/<pkg>/-/<file>`, all other fields (including `dist.integrity` and `dist.shasum`) are untouched. `GET /<pkg>/<version-or-tag>` is cut from the full packument.
- **Tarballs** (`GET /<pkg>/-/<file>`) are fetched from the URL the packument's `dist.tarball` names for that file (a file no version lists is `404` and never fetched), checked against `dist.integrity` (the strongest SRI algorithm present) or, when absent, `dist.shasum`, and only then cached. A mismatching download answers `502` and is not stored; a version that advertises no digest cannot be checked and is accepted, but cached for the repository TTL only. Verified tarballs are immutable. Upstream credentials are sent only to the configured upstream host, never to another host a packument points at.
- **Writes:** `npm publish`, `unpublish`, `deprecate` and `dist-tag` against a proxy answer `405` (`Allow: GET, HEAD`). Other registry endpoints (search, audit, login) are not proxied.
- With the upstream down, installs keep working from the cache: cached tarballs are always served, and a packument is served stale (`Warning: 110`) for up to `max_stale_seconds` past its expiry; a package never requested before answers `502`.

### Helm proxies

A Helm proxy mirrors a classic chart repository: `upstream_url` is the directory that holds `index.yaml` (for example `https://charts.jetstack.io`; there is no default). Clients use it like a hosted Helm repository ([client setup](../formats/helm.md#client-setup)): `helm repo add NAME HOST/repos/PROXY`.

- **`index.yaml`** is fetched from the upstream, cached as received with `cache_ttl_seconds` and served stale on upstream failure like every other proxy file. A body that does not parse as an index (an HTML error page, a truncated document, a document using YAML anchors) is `502` and not cached. Each response is rewritten: every chart `urls` entry, absolute or relative to the upstream (resolved the way helm does), becomes `BASE_URL/repos/PROXY/charts/<file>` where `<file>` is the last segment of the chart's URL path; all other fields (`digest`, `created`, `dependencies`, `annotations`, ...) are untouched. Chart versions that only list non-HTTP URLs (`oci://`, as the current Bitnami index does) are left out of the served index, because this server cannot serve them: use a docker proxy for those.
- **Charts** (`GET /charts/<file>`) are fetched from the URL the cached index lists for that file name (a file no version lists is `404` and never fetched; if two entries list different URLs ending in the same file name, the first keeps the plain name and the later one is listed as `<name>-<8 hex of a hash of its URL>.<ext>`), hashed while spooling and compared with the entry's `digest` (SHA-256 hex, an `sha256:` prefix is accepted); a mismatch or a `digest` that is not SHA-256 hex is `502` and nothing is stored. An entry without a `digest` is cached unverified, for the repository TTL only. Verified charts are immutable, so a second pull works with the upstream down.
- **Provenance:** `GET /charts/<file>.prov` (`helm pull --verify`) is fetched from next to the chart (the chart URL plus `.prov`) and cached with the repository TTL; it is not digest-checked.
- Upstream Basic credentials go only to the upstream's own origin: charts on another host (a CDN, GitHub release assets) are fetched without them.
- Uploading to a proxy (`POST /charts`) and every other write answer `405`.
- With the upstream down, `helm repo add`/`update` are answered from the cached index (stale, `Warning: 110`, for up to `max_stale_seconds`) and cached charts are always served; a chart never pulled before answers `502`.

### Cargo proxies

A cargo proxy (`upstream_url` such as `https://index.crates.io`, the default) is a sparse registry, not a path mirror. Point cargo at it with `sparse+HOST/repos/REPO/index/`; to use it for crates.io dependencies replace the source:

```toml
# ~/.cargo/config.toml
[source.crates-io]
replace-with = "oa"

[source.oa]
registry = "sparse+HOST/repos/REPO/index/"
```

- **`config.json`** (`GET /index/config.json`, also `/config.json`) is generated, not fetched: `dl` and `api` point at `BASE_URL/repos/REPO`, and a private repository adds `"auth-required": true` (cargo then sends its token with every request). It never touches the upstream.
- **Index files** (`GET /index/<prefix>/<crate>`, the usual `1/`, `2/`, `3/<c>/` and `<ab>/<cd>/` layout, lowercase) are fetched from the upstream, checked to be a well-formed newline-delimited JSON index (an HTML error page served with `200` is `502` and not cached), cached verbatim with the repository TTL and served stale on upstream failure. Responses carry the blob's `ETag`, so cargo revalidates with `If-None-Match`. Paths that do not follow the layout are `404` without an upstream request.
- **Crate files** (`GET /api/v1/crates/<crate>/<version>/download`) are fetched from the URL the upstream's own `config.json` `dl` template yields (for crates.io `https://static.crates.io/crates/<crate>/<version>/download`; the `{crate}`, `{version}`, `{prefix}`, `{lowerprefix}` and `{sha256-checksum}` markers are supported), after checking the version is listed in the crate's index file (a version it does not list is `404` and never fetched). The bytes must match the sha256 `cksum` of that index entry or they are refused with `502` and not cached. Cached crate files are immutable. Yanked versions stay downloadable, as upstream.
- Upstream credentials (Basic) go only to the configured upstream host, never to the host a `dl` template points at. Registries that need cargo's bare-token `Authorization` header are not supported as upstreams.
- **Writes:** `cargo publish`, `yank` and `unyank` against a proxy answer `405` (`Allow: GET, HEAD`); `cargo search` is not proxied (`404`).
- With the upstream down, builds keep working from the cache: cached crates are always served, an index file is served stale (`Warning: 110`) for up to `max_stale_seconds` past its expiry, and a crate never requested before answers `502`.

### NuGet proxies

A nuget proxy is a v3 feed, not a path mirror. `upstream_url` is the upstream **service index** (default `https://api.nuget.org/v3/index.json`); clients use `HOST/repos/REPO/v3/index.json` as their package source exactly as with a hosted nuget repository ([client setup](../formats/nuget.md#client-setup)). Only the resources the proxy serves are advertised, all pointing at `BASE_URL/repos/REPO/v3/...`: the registrations base, the flat container (`PackageBaseAddress`) and, when the upstream has them, search and autocomplete.

- **Service index** (`/v3/index.json`) is fetched from `upstream_url`, cached with the repository TTL and rewritten. The upstream resource URLs it names (flat container, registrations) are where the other requests go.
- **Registrations** (`/v3/registration/<id>/index.json`, pages, leaves) are cached as received (gzip from `registration5-gz-*` is decoded) and rewritten: `@id`, `parent`, `registration` and `packageContent` point at this repository; paged (non-inline) registrations are served through `/v3/registration/<id>/page/<lower>/<upper>.json`, and a page the index does not list is `404` and never fetched. Catalog URLs (`catalogEntry`) are left unchanged.
- **Flat container** (`/v3/package/<id>/index.json` version list, `<version>/<id>.<version>.nupkg`, `<id>.nuspec`) is fetched from the upstream's `PackageBaseAddress`. Ids and versions are matched in normalized form (lower case, `1.0` is `1.0.0`), so every spelling shares one cache entry.
- **Packages** (`.nupkg`) are checked before they are cached: against the SHA-512 (or SHA-256) `packageHash` and `packageSize` of the package's catalog entry, found through its registration leaf, and, when the upstream publishes none (a hosted OpenArtifact upstream, or a catalog entry that is gone), against being a zip with a root `.nuspec`. A mismatching download answers `502` and is not stored. `.nupkg` and `.nuspec` files are immutable once cached.
- **Search and autocomplete** (`/v3/query`, `/v3/autocomplete`) pass through uncached with their result URLs rewritten; `404` when the upstream service index has none.
- **Writes:** `dotnet nuget push`/`delete` against a proxy answer `405` (`Allow: GET, HEAD`).
- With the upstream down, restores keep working from the cache: cached packages are always served, and the service index, version lists and registrations are served stale (`Warning: 110`) for up to `max_stale_seconds` past their expiry; a package never requested before answers `502`.
- Upstream credentials are sent only to the origin of `upstream_url`. A feed whose resources live on another origin (Azure Artifacts' `pkgs.dev.azure.com` → `feeds.dev.azure.com`, nuget.org's search host) is reachable, but those requests are anonymous.

### Upstream URL, SSRF and credentials

`upstream_url` must be a plain `http(s)` base URL (no credentials, query or fragment); credentials go in `upstream_username`/`upstream_password`. It passes the same SSRF validation as webhooks, at configuration time and on every fetch and redirect: loopback, private and link-local addresses are refused. Set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` only for internal upstreams; the cloud metadata range (169.254.x) stays blocked. The blocked ranges also cover addresses that embed an IPv4 address in an IPv6 one (NAT64, 6to4, Teredo) and reserved IPv4 ranges, so they cannot be used to reach a blocked IPv4 destination. The `Authorization` header is dropped on any redirect that changes scheme or host (or port), so credentials never follow a redirect to another origin. At most a bounded number of upstream fetches run at the same time; further requests wait for a slot (or their own cancellation). **Changing the origin of `upstream_url` (scheme, host or port) or `upstream_username` in a `PATCH` clears the stored `upstream_password` unless the same request sends a new `upstream_password`**, so a maintainer cannot redirect a stored credential to a host they control. See [`../../.env.example`](../../.env.example).

### Go proxies

A go proxy mirrors a GOPROXY (`upstream_url`, default `https://proxy.golang.org`); clients use `GOPROXY=HOST/repos/REPO`. Module paths and versions are forwarded in the case-encoded form the go command uses (`!x`). `@v/list` and `@latest` use the repository TTL with serve-stale on upstream failure; `.info`, `.mod` and `.zip` of a canonical version are immutable. Fetched `.mod` and `.zip` files are verified against the Go checksum database, and bodies that are not what their path claims are refused (`502`, not cached). `GET /sumdb/...` is not proxied (`404`): the go command then asks `sum.golang.org` directly. Details and limits: [Go modules](../formats/go.md#proxy-and-group-repositories).

### Docker / OCI proxies

A docker proxy is a pull-through cache of an OCI registry and is served under `/v2/` like hosted docker repositories (it has no `/repos/{name}/...` file URLs). `docker pull HOSTPORT/REPO/library/alpine:3.20` fetches `library/alpine:3.20` from the upstream. On Docker Hub (`registry-1.docker.io`, the default) a single-segment image such as `HOSTPORT/REPO/alpine` is mapped to `library/alpine`. Only the scheme and host of `upstream_url` are used. See [Docker / OCI](../formats/docker.md#proxy-and-group-repositories) for client setup and behavior.

## Group repositories

A group is an ordered list of hosted and proxy members of the same format. Groups cannot be nested, cannot contain themselves or duplicates, and hold at most 200 members; violations are `400 validation_failed`.

- The first member that has the requested file answers. A member answering 404 is passed over, as is one failing with 5xx; when no member has the file the group answers `404`, or `502` if a member failed (so a client does not cache "does not exist" for a file a broken member might have).
- Each member is authorized for the caller as if requested directly (action `read`). A member the caller cannot read behaves like one without the file, so a public group never exposes a private member's files.
- Maven `maven-metadata.xml` (and its `.md5/.sha1/.sha256/.sha512` checksums) is merged across all readable members: versions are united and ordered, `latest`/`release`/`lastUpdated` recomputed. With one contributing member its document is served unchanged; an erroring member is ignored while another contributes.
- A pypi group merges `GET /simple/` (union of project names) and `GET /simple/<project>/` across readable hosted and pypi proxy members: the file list is the union by filename, and when several members list the same filename the earliest member wins. Links are rewritten to `/repos/GROUP/files/<project>/<filename>`, and a download is resolved first-hit in member order, so it comes from the member whose entry was listed. A member failing while another contributes is ignored, but the partial page is served with `Cache-Control: no-store`; with no contributor the group answers `404`, or `502` if a member failed. The group answers HTML or PEP 691 JSON by `Accept`, like a proxy.
- A Helm group merges `GET /index.yaml` across readable hosted and Helm proxy members: a chart name and version is listed once, from the earliest member that has it (the first member's entry, digest included, wins), and every chart URL is rewritten to `BASE_URL/repos/GROUP/charts/<file>`. Chart downloads (and `.prov` files) are resolved first-hit in member order. A member failing or serving an unreadable index while another contributes is ignored; with no contributor the group answers `404`, or `502` if a member failed. Only `index.yaml` and `charts/<file>` are served: a hosted member's chart listing (`GET /charts`) is not exposed.
- A cargo group generates its own `config.json` (`dl`/`api` point at the group) and merges each crate's index file across readable hosted and cargo proxy members: the union of versions (build metadata ignored when comparing), the earliest member winning a version both list, and every other line untouched. Crate downloads are resolved first-hit in member order, so a crate comes from the member whose index entry won the merge. A member failing while another contributes is ignored; with no contributor the group answers `404`, or `502` if a member failed or served something that is not an index file. Clients use `sparse+HOST/repos/GROUP/index/`.
- A go group answers `<module>/@v/list` with the union of the versions of all readable hosted and go proxy members (SemVer order, highest first, one line per version, failing members ignored while another answers); everything else (`.info`, `.mod`, `.zip`, `@latest`) is first-hit in member order.
- A nuget group serves its own service index (registrations and flat container of the group; no search, `/v3/query` and `/v3/autocomplete` are `404`), unites `GET /v3/package/<id>/index.json` version lists, and merges `GET /v3/registration/<id>/index.json` across readable hosted and nuget proxy members: one item per version (the earliest member wins), ordered by version, in inline pages of 64; the pages a member does not inline are fetched from it, and a member that fails to deliver its registration whole is left out. Registration leaves and pages come from the first member that has them and `.nupkg`/`.nuspec` downloads are first-hit; every URL points at the group. A member failing while another contributes is ignored; with no contributor the group answers `404`, or `502` if a member failed.
- Deb and RPM groups generate their own, merged metadata (see [Deb and RPM groups](#deb-and-rpm-groups)); package files are served by the member the merged index lists them for.
- Docker groups resolve manifests and blobs from the first readable member that has them and merge the `tags/list` of all readable members (sorted, de-duplicated, paginated with `n`/`last`; `HEAD` is answered like `GET` without the body).
- Every merged answer (npm packuments, Helm `index.yaml`, cargo index files, go `@v/list`, NuGet version lists and registrations, pypi pages) that left out a failed member is incomplete: it is served with `Cache-Control: no-store` and without an `ETag`, so clients and caches do not keep it. A complete merge carries an `ETag` and revalidates.
- <a id="hosted-members-win-a-package-name"></a>**Hosted members win a package name (`merge_proxy_versions`, default `false`).** This protects against dependency confusion: if a readable hosted member of the group holds a package name, a public package of the same name on a proxy member can no longer add versions, metadata entries or files under that name. Names no hosted member holds are served by proxy members as before. The rule applies per package name, not per version: once a hosted member holds any version of `acme-lib`, no proxy version of `acme-lib` is visible, even a higher one. A hosted member the caller cannot read, or that is blocked, counts as absent. Setting `merge_proxy_versions` to `true` restores the union of all members (and the risk). Per format, the hidden items are:
  - npm: the packument of the name lists only hosted versions, and tarballs of the name are served only from hosted members;
  - pypi: the project page lists only hosted files, and file downloads of the project come only from hosted members;
  - go: `@v/list` lists only hosted versions, and `.info`, `.mod` and `.zip` of the module come only from hosted members (`@latest` likewise);
  - cargo: the crate's index file contains only hosted versions, and `.crate` downloads come only from hosted members;
  - nuget: registration and flat-container version lists (and leaves, `.nupkg`, `.nuspec`) of the id come only from hosted members;
  - maven: `maven-metadata.xml` of the artifact is built from hosted members only, and files under that group/artifact path are served only from hosted members;
  - helm: `index.yaml` lists only hosted versions of a chart name, and chart/`.prov` downloads of that name come only from hosted members;
  - docker: `tags/list`, manifests and blobs of a repository path held by a hosted member come only from hosted members.
  
  raw groups (and any other format whose group answers first-hit) do not merge versions: the first member by order that has the file wins, unchanged by this setting.
- Group URLs are the same as hosted ones: `HOST/repos/maven/GROUP` for Maven, `HOST/repos/raw/GROUP/<path>` for raw, `HOST/repos/GROUP/simple/` for PyPI, `sparse+HOST/repos/GROUP/index/` for cargo, `GOPROXY=HOST/repos/GROUP` for Go, `HOST/repos/GROUP` for Helm, `HOST/repos/GROUP/v3/index.json` for NuGet, `HOSTPORT/GROUP/<image>:<tag>` for docker, `HOST/repos/GROUP` for Debian (`deb [signed-by=…] HOST/repos/GROUP <dist> <component>`) and RPM (`baseurl=HOST/repos/GROUP`).

### Deb and RPM groups

Debian and RPM clients read an index, so a group cannot forward the request to one member: it publishes an index of its own, generated from the members and stored like any other generated metadata.

- **Merging.** The group's `Release`/`InRelease`/`Release.gpg` and `Packages.gz`/`Packages.xz` (Debian), or `repomd.xml`/`repomd.xml.asc` with `primary`, `filelists`, `other` and `modules` (RPM), list every package of every member. Hosted members contribute what is in their database, proxy members what their upstream publishes (read through the proxy cache). Member order decides: a package version (Debian: name, version and architecture; RPM: name, epoch, version, release and architecture) offered by several members is listed once, from the earliest member. The same hosted-wins rule as the other formats applies unless `merge_proxy_versions` is `true`: a package name held by a hosted member is not offered from proxy members.
- **Downloads** come from the member the index lists the file for, so the file a client downloads is always the one whose checksum the index carries. A request for a file the index does not list is `404`, even if some member has it.
- **Signing.** The group's metadata is signed with the same server-wide key as hosted repositories ([signing](../security/signing.md)); `HOST/repos/GROUP/public.key` serves its public key. Upstream signatures are not passed through, because the merged index is a new document. Without a key the group is unsigned, exactly like a hosted repository.
- **Access control.** A caller is served an index that lists only the members they can read, and downloads are authorized against the owning member. Callers who can read every member share one index, generated in the background as above. A caller who can read only some members gets an index (with its own signature) generated for exactly that set of members on their first request, which waits for the generation like the first request of a group does; these per-set indexes are kept until the group is regenerated, which drops them, so the next such request generates it again. A member the caller cannot read behaves as if it were not in the group, including for the hosted-wins rule. A caller who can read no member gets `404`. The signing key and `public.key` are the same for all callers.
- **Regeneration.** The index is regenerated in the background when a hosted member changes (upload, delete, trash, restore, cleanup), when the group's members or settings change, when a member is deleted or blocked, and at startup for groups that have no index yet. Proxy members have no change signal, so a request that finds the index older than the shortest `cache_ttl_seconds` among them (at least one minute) serves it and refreshes it in the background. A proxy member that cannot be reached is left out of that run, the run is marked partial and retried after a minute; an upstream that simply does not have a distribution is a complete answer. The first request for a group that has no index yet waits for the generation (at most two minutes, then `503` with `Retry-After`), which for a group of wide upstreams (a full distribution mirror) downloads their indexes once; later runs reuse the proxy cache.
- **Debian distributions.** A group publishes the distributions that were requested and exist in at least one member (the hosted members' distributions and the proxies' upstream releases); asking for one that no member has is `404`. The component/architecture indexes follow the union of what the members declare.
- **Not merged or served by a group:** Debian `by-hash` (the plain `Packages` file is served too, decompressed from the stored `Packages.gz`, because `Release` lists it as apt requires), `Contents`, `Sources` and translations; RPM `comps.xml`, `updateinfo` and the sqlite databases, and `xml:base` redirects of upstream packages (dropped, so clients cannot be sent to another host). Modules of RPM members are merged (the first member wins a module/stream/version/context, and so do module defaults per module).

## Public repositories and RBAC

`public: true` grants anonymous `read` (never write) on that repository, independent of the global `allowAnonymousRead` setting. Everything else is decided by RBAC: `/repos/{repo}/...` maps to read/write by HTTP method and `/api/v1/*` to `manage`, against the caller's permission targets. Group members are checked individually, as above. Details: [RBAC](../security/rbac.md). Package clients authenticate with an API token (Bearer, or Basic with the token as password); account passwords only work for the web UI login.
