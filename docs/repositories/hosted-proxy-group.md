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
| raw | yes | yes | yes |
| deb | yes | yes | no |
| rpm | yes | yes | no |
| npm, docker, helm, pypi, nuget, go, cargo, rubygems, conda, conan, ivy, sbt | yes | no | no |

Creating a proxy or group repository of an unsupported format is rejected with `400 validation_failed`. Deb and RPM groups are rejected because their `Packages`/`Release`/`repodata` metadata is generated per repository and cannot be merged across members. Proxy and group repositories are read-only: writes (client uploads and the `/api/v1/repositories/{repo}/{debs,packages}` upload endpoints) answer `405` with `Allow: GET, HEAD`. Client URLs per format are on the [formats](../formats/README.md) pages.

## Creating repositories

Creating, updating and deleting repositories needs an admin principal (`403 forbidden` otherwise). Names match `^[a-zA-Z0-9_-]{1,100}$`; `format` is one of the 16 formats above.

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
| `upstream_url` | proxy (required) | Plain `http(s)` base URL. |
| `upstream_username`, `upstream_password` | proxy | HTTP Basic credentials sent upstream. Stored encrypted, never returned. |
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

Requests are mirrored by path from `upstream_url`. A file is fetched on first request, stored content-addressed in the blob store, recorded under the repository (it appears in the package list and can be downloaded there) and served from the cache afterwards. Path segments that are empty, `.`, `..`, contain backslashes or control characters, and directory paths (trailing `/`) answer `404`; upstream requests carry `Accept-Encoding: identity`, so bytes are mirrored unchanged.

### Cache TTLs

- `cache_ttl_seconds` bounds how long mutable files are served without refetching: Maven `maven-metadata.xml` and `-SNAPSHOT` files, Debian `dists/` and RPM `repodata/` metadata, and everything in raw repositories.
- Released Maven artifacts and `.deb`/`.udeb`/`.rpm` files are immutable once cached and never revalidated.
- An upstream 404/410 is cached for `negative_cache_ttl_seconds` and drops any cached copy of the file (it is never served stale).

### Upstream failures and serve-stale

Any upstream failure other than 404/410 answers `502` with a generic message and caches nothing, except as described here: 401/403, a timeout, a redirect to a disallowed address, a file over 4 GiB, or 5xx.

When an expired entry cannot be revalidated because the upstream is unreachable, times out (30 s for response headers), answers 5xx or breaks the transfer, the cached copy is served with `200` and `Warning: 110 - "Response is Stale"`, and a warning is logged. This holds for up to `max_stale_seconds` past the entry's expiry; with `0` the client gets `502`. A successful refetch replaces the entry. 401/403 from the upstream are not treated as an outage and stay `502`.

The proxy cache worker runs every 5 minutes and deletes entries expired by more than one hour. Cached files are additionally kept for the repository's `max_stale_seconds`, so a larger value trades disk for availability. Blobs no other cache entry references are then deleted.

### Behavior notes

- Concurrent requests for the same uncached path share one upstream fetch. Clients get the first byte once the whole file is cached.
- Cache hits do not update `hit_count` or `last_hit_at`: that would add a database write to every download.
- Quarantined files answer `403` (`ARTIFACT_QUARANTINED`) like hosted ones.

### Upstream URL, SSRF and credentials

`upstream_url` must be a plain `http(s)` base URL (no credentials, query or fragment); credentials go in `upstream_username`/`upstream_password`. It passes the same SSRF validation as webhooks, at configuration time and on every fetch and redirect: loopback, private and link-local addresses are refused. Set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` only for internal upstreams; the cloud metadata range (169.254.x) stays blocked. See [`../../.env.example`](../../.env.example).

## Group repositories

A group is an ordered list of hosted and proxy members of the same format. Groups cannot be nested, cannot contain themselves or duplicates, and hold at most 200 members; violations are `400 validation_failed`.

- The first member that has the requested file answers. A member answering 404 is passed over, as is one failing with 5xx; when no member has the file the group answers `404`, or `502` if a member failed (so a client does not cache "does not exist" for a file a broken member might have).
- Each member is authorized for the caller as if requested directly (action `read`). A member the caller cannot read behaves like one without the file, so a public group never exposes a private member's files.
- Maven `maven-metadata.xml` (and its `.md5/.sha1/.sha256/.sha512` checksums) is merged across all readable members: versions are united and ordered, `latest`/`release`/`lastUpdated` recomputed. With one contributing member its document is served unchanged; an erroring member is ignored while another contributes.
- Group URLs are the same as hosted ones: `HOST/repos/maven/GROUP` for Maven, `HOST/repos/raw/GROUP/<path>` for raw.

## Public repositories and RBAC

`public: true` grants anonymous `read` (never write) on that repository, independent of the global `allowAnonymousRead` setting. Everything else is decided by RBAC: `/repos/{repo}/...` maps to read/write by HTTP method and `/api/v1/*` to `manage`, against the caller's permission targets. Group members are checked individually, as above. Details: [RBAC](../security/rbac.md). Package clients authenticate with an API token (Bearer, or Basic with the token as password); account passwords only work for the web UI login.
