<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Hosted, proxy and group repositories

Every repository has a format and a type (`repo_type`): `hosted` (default), `proxy` or `group`. Proxy and group repositories are served on the same client URLs as hosted ones.

## Type and format support

| Format | Hosted | Proxy | Group |
|---|---|---|---|
| maven | yes | yes | yes |
| npm | yes | yes | yes |
| raw | yes | yes | yes |
| pypi | yes | yes | yes |
| deb | yes | yes | no |
| rpm | yes | yes | no |
| docker | yes | yes | yes |
| helm, nuget, go, cargo, rubygems, conda, conan, ivy, sbt | yes | no | no |

Creating a proxy or group repository of an unsupported format is rejected with `400 validation_failed`. Deb and RPM groups are rejected because their `Packages`/`Release`/`repodata` metadata is generated per repository and cannot be merged across members. Proxy and group repositories are read-only: writes (client uploads and the `/api/v1/repositories/{repo}/{debs,packages}` upload endpoints) answer `405` with `Allow: GET, HEAD`. Client URLs per format are on the [formats](../formats/README.md) pages.

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
| `upstream_url` | proxy (required, except docker and pypi) | Plain `http(s)` base URL. Optional for docker (defaults to Docker Hub, `https://registry-1.docker.io`) and pypi (defaults to `https://pypi.org/simple`). |
| `upstream_username`, `upstream_password` | proxy | HTTP Basic credentials sent upstream (for docker: sent to the registry's token service). Stored encrypted, never returned. |
| `cache_ttl_seconds` | proxy | Default 3600 (1 h), max 2592000 (30 d). |
| `negative_cache_ttl_seconds` | proxy | Default 60, max 86400 (1 d). |
| `max_stale_seconds` | proxy | Default 604800 (7 d), max 31536000 (365 d), `0` disables serve-stale. |
| `member_repos` | group | Member repository names in priority order, at most 200. |
| `trash_retention_days` | all | Days deleted items stay in the [trash](../operations/trash.md). Integer `>= 0`, or `null`/omitted for the server default (`OPENARTIFACT_TRASH_RETENTION`, 7); `0` deletes permanently and immediately. Negative is `400`. |
| `immutable_releases` | all | Default `false`. When `true`, deleting or overwriting a released version is `409` unless an admin passes `?force=true` ([per format](../operations/trash.md#immutable-releases)). |

- For `cache_ttl_seconds` and `negative_cache_ttl_seconds`, `0` or an omitted field selects the default (it does not disable caching); negative values are `400`; values above the max are clamped. For `max_stale_seconds` only an omitted field selects the default: an explicit `0` means "never serve stale"; negative is `400`.
- `upstream_*`, TTL fields and `member_repos` on a repository of another type are `400 validation_failed`. A name that already exists is `409 conflict`, including the name of a repository that is still in the [trash](../operations/trash.md#repositories) (names stay reserved until the repository is purged or restored).
- The response shows `upstream_url`, `upstream_username` and the three TTL fields for proxies only; the password is never included.

Update with `PATCH /api/v1/repository/{repo}` (note the singular `repository`; admin only). Accepted fields: `description`, `public`, `blocked`, `trash_retention_days`, `immutable_releases`, the proxy fields above (`upstream_url` may not be emptied), and `member_repos` for groups (replaces the list; `[]` clears it, an absent field leaves it unchanged). Name, format and type cannot change. A blocked repository answers `403` on proxy requests and is skipped as a group member. `GET /api/v1/repository/{repo}` returns one repository; `GET /api/v1/repositories` lists the ones the caller may read.

Every repository type also accepts `presign_downloads` on create and `PATCH`: `true` redirects blob downloads to presigned object-store URLs, `false` always streams, `null` inherits the global `storage.presignDownloads` (in a `PATCH`, omit it to leave it unchanged). It only has an effect on S3 and GCS storage; see [Presigned URLs](../operations/storage.md#presigned-urls).

Group membership can also be managed on its own:

```sh
curl HOST/api/v1/repositories/maven-all/members -H "Authorization: Bearer <TOKEN>"
curl -X PUT HOST/api/v1/repositories/maven-all/members -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' -d '{"member_repos":["releases","maven-central"]}'
```

Each entry has `id`, `name`, `format`, `repo_type`, `priority`. `DELETE /api/v1/repository/{repo}` needs the repository name as confirmation (`?confirm=<name>` or a JSON body `{"confirm":"<name>"}`; `400 confirmation_required` without it) and returns `409` while a release bundle or promotion still references the repository (see [release bundles](release-bundles.md)). Otherwise the repository moves to the [trash](../operations/trash.md) and is hidden everywhere; when its retention expires (or immediately, with `trash_retention_days` 0) the purge removes the packages and reclaims orphaned blobs.

## Proxy repositories

Requests are mirrored by path from `upstream_url` (npm proxies are the exception, see [npm proxies](#npm-proxies)). A file is fetched on first request, stored content-addressed in the blob store, recorded under the repository (it appears in the package list and can be downloaded there) and served from the cache afterwards. Path segments that are empty, `.`, `..`, contain backslashes or control characters, and directory paths (trailing `/`) answer `404`; upstream requests carry `Accept-Encoding: identity`, so bytes are mirrored unchanged.

### Cache TTLs

- `cache_ttl_seconds` bounds how long mutable files are served without refetching: Maven `maven-metadata.xml` and `-SNAPSHOT` files, Debian `dists/` and RPM `repodata/` metadata, PyPI simple index and project pages, and everything in raw repositories.
- Released Maven artifacts, `.deb`/`.udeb`/`.rpm` files and PyPI distribution files (and their `.metadata`) and npm tarballs are immutable once cached and never revalidated.
- An upstream 404/410 is cached for `negative_cache_ttl_seconds` and drops any cached copy of the file (it is never served stale).

### Upstream failures and serve-stale

Any upstream failure other than 404/410 answers `502` with a generic message and caches nothing, except as described here: 401/403, a timeout, a redirect to a disallowed address, a file over 4 GiB, or 5xx.

When an expired entry cannot be revalidated because the upstream is unreachable, times out (30 s for response headers), answers 5xx or breaks the transfer, the cached copy is served with `200` and `Warning: 110 - "Response is Stale"`, and a warning is logged. This holds for up to `max_stale_seconds` past the entry's expiry; with `0` the client gets `502`. A successful refetch replaces the entry. 401/403 from the upstream are not treated as an outage and stay `502`.

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
- A distribution is fetched on first download, hashed while spooling and compared with the sha256 the index advertises (and `.metadata` with the metadata hash); a mismatch is `502`, nothing is stored. A file with no sha256 in the index is cached unverified. Cached files are immutable, so a second install works with the upstream down.
- The upstream's Basic credentials go only to the upstream's own origin: files hosted on another origin (for example `files.pythonhosted.org`) are fetched without them.
- Uploads (`twine upload` to `/legacy/`) are `405`.

### npm proxies

An npm proxy (`upstream_url` such as `https://registry.npmjs.org`) is a registry, not a path mirror: clients use `HOST/repos/REPO/` as their registry exactly as with a hosted npm repository ([client setup](../formats/npm.md#client-setup)).

- **Packuments** (`GET /<pkg>`, `GET /@scope%2fname`, also `@scope/name`) are fetched from the upstream, cached as received with the repository TTL (`cache_ttl_seconds`) and served stale on upstream failure like every other proxy file. A client sending `Accept: application/vnd.npm.install-v1+json` (npm, pnpm and yarn do) gets the abbreviated document; it and the full document are cached separately. Each response is rewritten: every `dist.tarball` points at `BASE_URL/repos/REPO/<pkg>/-/<file>`, all other fields (including `dist.integrity` and `dist.shasum`) are untouched. `GET /<pkg>/<version-or-tag>` is cut from the full packument.
- **Tarballs** (`GET /<pkg>/-/<file>`) are fetched from the URL the packument's `dist.tarball` names for that file (a file no version lists is `404` and never fetched), checked against `dist.integrity` (the strongest SRI algorithm present) or, when absent, `dist.shasum`, and only then cached. A mismatching download answers `502` and is not stored; a version that advertises no digest cannot be checked and is accepted. Cached tarballs are immutable. Upstream credentials are sent only to the configured upstream host, never to another host a packument points at.
- **Writes:** `npm publish`, `unpublish`, `deprecate` and `dist-tag` against a proxy answer `405` (`Allow: GET, HEAD`). Other registry endpoints (search, audit, login) are not proxied.
- With the upstream down, installs keep working from the cache: cached tarballs are always served, and a packument is served stale (`Warning: 110`) for up to `max_stale_seconds` past its expiry; a package never requested before answers `502`.

### Upstream URL, SSRF and credentials

`upstream_url` must be a plain `http(s)` base URL (no credentials, query or fragment); credentials go in `upstream_username`/`upstream_password`. It passes the same SSRF validation as webhooks, at configuration time and on every fetch and redirect: loopback, private and link-local addresses are refused. Set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` only for internal upstreams; the cloud metadata range (169.254.x) stays blocked. See [`../../.env.example`](../../.env.example).

### Docker / OCI proxies

A docker proxy is a pull-through cache of an OCI registry and is served under `/v2/` like hosted docker repositories (it has no `/repos/{name}/...` file URLs). `docker pull HOSTPORT/REPO/library/alpine:3.20` fetches `library/alpine:3.20` from the upstream. On Docker Hub (`registry-1.docker.io`, the default) a single-segment image such as `HOSTPORT/REPO/alpine` is mapped to `library/alpine`. Only the scheme and host of `upstream_url` are used. See [Docker / OCI](../formats/docker.md#proxy-and-group-repositories) for client setup and behavior.

## Group repositories

A group is an ordered list of hosted and proxy members of the same format. Groups cannot be nested, cannot contain themselves or duplicates, and hold at most 200 members; violations are `400 validation_failed`.

- The first member that has the requested file answers. A member answering 404 is passed over, as is one failing with 5xx; when no member has the file the group answers `404`, or `502` if a member failed (so a client does not cache "does not exist" for a file a broken member might have).
- Each member is authorized for the caller as if requested directly (action `read`). A member the caller cannot read behaves like one without the file, so a public group never exposes a private member's files.
- Maven `maven-metadata.xml` (and its `.md5/.sha1/.sha256/.sha512` checksums) is merged across all readable members: versions are united and ordered, `latest`/`release`/`lastUpdated` recomputed. With one contributing member its document is served unchanged; an erroring member is ignored while another contributes.
- A pypi group merges `GET /simple/` (union of project names) and `GET /simple/<project>/` across readable hosted and pypi proxy members: the file list is the union by filename, and when several members list the same filename the earliest member wins. Links are rewritten to `/repos/GROUP/files/<project>/<filename>`, and a download is resolved first-hit in member order, so it comes from the member whose entry was listed. A member failing while another contributes is ignored, but the partial page is served with `Cache-Control: no-store`; with no contributor the group answers `404`, or `502` if a member failed. The group answers HTML or PEP 691 JSON by `Accept`, like a proxy.
- Docker groups resolve manifests and blobs from the first readable member that has them and merge the `tags/list` of all readable members (sorted, de-duplicated, paginated with `n`/`last`).
- Group URLs are the same as hosted ones: `HOST/repos/maven/GROUP` for Maven, `HOST/repos/raw/GROUP/<path>` for raw, `HOST/repos/GROUP/simple/` for PyPI, `HOSTPORT/GROUP/<image>:<tag>` for docker.

## Public repositories and RBAC

`public: true` grants anonymous `read` (never write) on that repository, independent of the global `allowAnonymousRead` setting. Everything else is decided by RBAC: `/repos/{repo}/...` maps to read/write by HTTP method and `/api/v1/*` to `manage`, against the caller's permission targets. Group members are checked individually, as above. Details: [RBAC](../security/rbac.md). Package clients authenticate with an API token (Bearer, or Basic with the token as password); account passwords only work for the web UI login.
