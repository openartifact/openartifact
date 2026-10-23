<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Performance

Benchmarks, load testing and profiling tools, and the settings that affect throughput.

## Benchmarks

Two layers, both driven from the repository root.

### Go micro-benchmarks

```sh
make bench    # go test -bench=. -benchmem -run=^$ ./...
```

Runs every `Benchmark*` function with allocation reporting: package parsers (`pkg/deb`, `pkg/maven`, `pkg/npm`, `pkg/rpm`), metadata builders (`internal/format/{deb,helm,maven,npm,pypi,rpm}`), auth, RBAC middleware, JSON encoding, search and workers. Narrow it with a package path, for example `go test -bench=. -benchmem -run='^$' ./internal/auth/`.

### HTTP load scenarios

```sh
OA_URL=http://127.0.0.1:8080 OA_USER=admin OA_PASS=... OA_PID=<server pid> make bench-load
```

`make bench-load` runs [`bench/run.sh`](../../bench/run.sh), which:

1. builds `bench/oabench` into `$OA_OUT` (default `bench-out/`);
2. seeds the instance once (skipped while `$OA_OUT/state.json` exists; delete it to reseed). Seeding is idempotent: it accepts existing repositories and packages;
3. resets `pg_stat_statements` when `PSQL` is set;
4. runs `oabench load` over all scenarios and prints a line each, also saved as `$OA_OUT/result.txt` and `$OA_OUT/result.json`;
5. prints the top 15 statements from `pg_stat_statements` into `$OA_OUT/pg_stat_statements.txt`, when `PSQL` is set.

Run it against a throwaway instance. Seeding writes thousands of artifacts and always tries to change the password of `OA_USER` from `OA_PASS` to `-new-pass` (default `Bench-Passw0rd-Strong!42`), which is what the forced first-login change requires. If `OA_PASS` is rejected on a later run, the seeder retries with that password. To leave passwords alone, run `oabench seed` directly with `-token <TOKEN>`. The seeder mints an access token named `bench-<unix time>` and stores it in `state.json`, so keep `bench-out/` out of version control.

| Variable | Default | Meaning |
|---|---|---|
| `OA_URL` | required | server base URL |
| `OA_USER`, `OA_PASS` | required | admin account used to log in and mint the token |
| `OA_PID` | unset | server PID, for RSS and CPU sampling (read from `/proc`, so Linux and the same host) |
| `OA_DURATION` | `8s` | measured time per scenario |
| `OA_CONCURRENCY` | `32` | concurrent clients per scenario |
| `OA_OUT` | `bench-out` | output directory |
| `OA_ONLY` | unset | comma-separated, case-insensitive substrings; run matching scenarios only (for example `OA_ONLY=npm,search`) |
| `PSQL` | unset | a `psql` command line that reaches the server's database, for example `PSQL="psql -h ... -U ... db"`; enables the `pg_stat_statements` capture |

#### Seed data

`oabench seed` creates the repositories `bench-maven`, `bench-npm`, `bench-deb`, `bench-rpm` and `bench-docker` and fills them with 3000 Maven artifacts (jar and pom), 1500 npm packages and 400 deb packages. It adds "hot" items for the metadata scenarios (a Maven artifact with 200 versions, an npm package with 100 versions), a 10 MB Maven jar and a Docker image with one 8 MB layer. Flags: `-url`, `-user`, `-pass`, `-new-pass`, `-token` (reuse a token instead of logging in), `-state`, `-maven`, `-npm`, `-deb`, `-rpm FILE`, `-workers` (default 16).

`run.sh` does not pass `-rpm`, so `bench-rpm` stays empty and the rpm scenario measures an empty repository. To seed an RPM, run `oabench seed ... -rpm pkg/rpm/testdata/weakdeps.rpm` yourself first.

#### Scenarios

27 scenarios, each with a 1 s unmeasured warm-up first:

| Group | Scenarios |
|---|---|
| Downloads | Maven jar (8 KB), Maven jar (10 MB), npm tarball, Docker blob (8 MB), Docker manifest |
| Metadata | `maven-metadata.xml` (200 versions), npm packument (100 versions), abbreviated npm packument, deb `Packages.gz`, deb `Release`, rpm `repomd.xml` |
| Conditional metadata | `maven-metadata.xml`, npm packument, deb `Release`, rpm `repomd.xml` sent with `If-None-Match` (304 path; servers without an ETag see plain GETs) |
| API | `GET /auth/me`, `GET /repositories`, package lists for Maven and npm (`limit=100`) |
| Search | `q=bench`, `q=art1` |
| Dashboard | audit log, system info, SPA `index.html` |
| Uploads | Maven `PUT` (10 MB), npm publish, deb upload |

Heavy scenarios are capped below `OA_CONCURRENCY`: 8 clients for the 10 MB jar and 8 MB Docker blob downloads, 4 for the 10 MB upload.

#### Output columns

Per scenario: concurrency (`c`), requests (`n`), error rate (HTTP 400 and above or transport errors), requests per second, p50, p95 and p99 latency in ms, MB/s received, server CPU percent, peak server RSS in MB, and allocations and bytes per request. The JSON file adds max latency and the RSS high-water mark.

- RSS and CPU need `OA_PID` and are sampled from `/proc/<pid>`; without it they show 0.
- Allocations per request are the change in the server's `go_memstats_mallocs_total` and `go_memstats_alloc_bytes_total` from `/metrics` divided by the request count. They are process-wide, so run nothing else against the server. `run.sh` scrapes `/metrics` without a token, so start the server with `METRICS_ALLOW_ANONYMOUS=true`; when running `oabench load` directly, pass `-metrics URL -metrics-token <METRICS_AUTH_TOKEN>` instead. See [monitoring](monitoring.md#metrics). Unreachable metrics show 0 allocations.
- With `PSQL` set, `pg_stat_statements` must be loaded (`shared_preload_libraries = 'pg_stat_statements'`, then `CREATE EXTENSION pg_stat_statements` in the database).
- Leave `OPENARTIFACT_RATE_LIMIT_RPS` at `0`: the load generator is one client IP, and throttled requests count as errors.

#### Environment pitfalls

Do not benchmark against a Postgres behind a rootless-container port forwarder: it inflates large responses by orders of magnitude. Use host networking or a native install.

### Running `oabench` directly

```sh
go build -o bench-out/oabench ./bench/oabench
bench-out/oabench seed -url HOST -user admin -pass '...' -state bench-out/state.json
bench-out/oabench load -state bench-out/state.json -duration 15s -concurrency 32 -pid <server pid> -only npm
```

`load` flags: `-state`, `-duration` (10s), `-concurrency` (32), `-warmup` (1s), `-only`, `-pid`, `-metrics`, `-metrics-token`, `-pprof`, `-pprof-token`, `-profile-dir`, `-json FILE`, `-label` (stored in the JSON).

## Profiling

Start the server with the profiler on (off by default; it costs CPU and exposes internals):

```sh
OPENARTIFACT_PPROF_ENABLED=true     # or server.pprofEnabled: true
```

Profiles are served under `/api/v1/admin/pprof/` (`profile`, `heap`, `allocs`, `mutex`, `block`, `goroutine`, `trace`, `cmdline`, `symbol`). The route needs an admin token: deploy tokens and anonymous callers are refused. While enabled, mutex profiling samples 1 in 100 contended events and block profiling samples blocking events of 100 µs or more; the server logs a warning at startup. Switch it off again after diagnosing.

```sh
curl -H "Authorization: Bearer <TOKEN>" -o cpu.pb.gz "HOST/api/v1/admin/pprof/profile?seconds=30"
go tool pprof -http=: cpu.pb.gz
```

To capture profiles for every load scenario:

```sh
bench-out/oabench load -state bench-out/state.json \
  -pprof HOST/api/v1/admin/pprof -pprof-token <TOKEN> -profile-dir profiles
```

For each scenario this writes `<scenario>.{cpu,heap,allocs,mutex,block,goroutine}.pb.gz` into `profiles/`. The CPU profile covers the measured window minus one second; the others are taken right after it. Both `-pprof` and `-profile-dir` are required. Open a file with `go tool pprof -http=: profiles/<file>.pb.gz`. The `-pprof` help text in `oabench` mentions `/debug/pprof`; the correct base is `/api/v1/admin/pprof`.

## Tuning

| Setting | Environment variable | YAML key | Default |
|---|---|---|---|
| Max open DB connections | `DB_MAX_OPEN_CONNS` | `database.maxOpenConns` | 25 |
| Warm DB connections | `DB_MAX_IDLE_CONNS` | `database.maxIdleConns` | 5 |
| DB connection lifetime | `DB_CONN_MAX_LIFETIME_MINUTES` | `database.connMaxLifetimeMinutes` | 30 |
| Auth lookup cache | `DB_AUTH_CACHE_TTL_MS` | `database.authCacheTtlMs` | 2000 |
| Metadata regeneration workers | `OPENARTIFACT_METADATA_REGEN_CONCURRENCY` | `workers.metadataRegenConcurrency` | 4 |
| Per-IP rate limit | `OPENARTIFACT_RATE_LIMIT_RPS` | `server.rateLimitRps` | 0 (off) |
| Request read timeout (seconds) | `SERVER_READ_TIMEOUT` | `server.readTimeout` | 0 (unlimited) |
| Response write timeout (seconds) | `SERVER_WRITE_TIMEOUT` | `server.writeTimeout` | 0 (unlimited) |
| Profiler | `OPENARTIFACT_PPROF_ENABLED` | `server.pprofEnabled` | off |

Read and write timeouts default to 0 so large uploads and downloads are never cut off; slow-header protection comes from `SERVER_READ_HEADER_TIMEOUT` (default 20 s). The full list is in [`.env.example`](../../.env.example) and [`openartifact.example.yaml`](../../openartifact.example.yaml).

### Database indexes and queries

- Package search (`/api/v1/search`, the `name`/`version`/`description` filters) matches with `ILIKE '%term%'` and is served by GIN trigram indexes (`idx_*_trgm`, extension `pg_trgm`, created by migration `030`). If you restore a dump into a database without the extension, search falls back to sequential scans (correct, but 16-35x slower on 300k blobs and 250k package rows).
- The repository half of the search matches `repositories.name` with `ILIKE '%term%'` too, and is served by `idx_repositories_name_trgm` (migration `040`). Results are ranked in the query itself (exact name, prefix, other substring, then newest, then id); the ranking is only computed while the match count is within the 10000-row cap, so a query that is refused as too broad costs what it did before ranking.
- `group=package` (the command palette) returns each package once, with its latest version, and counts packages.
- File-name lookups for deb, rpm and PyPI downloads use `(repository_id, filename)` indexes.
- The proxy cache eviction deletes expired entries in batches of 2000 and skips the table scan entirely when nothing expired more than an hour ago.
- The repository package listing (`GET /api/v1/repositories/{name}/packages`) counts the matching rows only when the page is full (or past the end); a short page, the common case for small repositories, derives `total` from the page itself. Large repositories still pay one `COUNT` per full page.
- The npm worker computes the `latest` dist-tag from version numbers and a deprecation flag read in SQL, so a publish no longer loads every manifest of the package.
- A group request resolves its members with one query instead of one per member.

### Auth lookup cache

Every authenticated request looks up its token, user, groups, permission targets and the repository by name. `DB_AUTH_CACHE_TTL_MS` keeps those results in memory for that many milliseconds (default 2000), saving several database round trips per request. `0` disables the cache.

- Changes made through this instance apply immediately: the auth-related writes (tokens, users, groups, permissions, repositories, SSO) clear the cache.
- Other replicas see a change after at most the TTL. So a revoked token, a disabled user or a removed permission can keep working on another replica for up to the TTL. The same holds for changes made outside the server process, such as the `resetpw` command.
- Search follows the same cache for the set of repositories a user may read, which each search otherwise rebuilds with a permission check per repository. The set is kept per principal (user, token kind and scopes, groups; never shared between principals), for the TTL, and dropped by the same writes. A set computed while such a write happened is not kept.
- Only positive results are cached for tokens, users and repositories, so creating one is visible at once. Each cache is bounded to 4096 entries and is emptied when the bound is hit.

For a single instance the default is safe. Lower the TTL or set `0` when a multi-replica deployment needs revocations to take effect everywhere at once.

### Database connections

Two pools talk to PostgreSQL, a pgx pool for the generated queries and a `database/sql` handle for the rest. Each is capped at `DB_MAX_OPEN_CONNS`, so one instance can open up to twice that many connections. Size PostgreSQL `max_connections` for `2 x DB_MAX_OPEN_CONNS x replicas` plus headroom. `DB_MAX_IDLE_CONNS` is the pgx pool's warm minimum; the `database/sql` handle keeps idle connections up to the open limit and closes surplus ones after 5 minutes idle, so bursts do not reconnect. Blob-lock transactions use at most half of `DB_MAX_OPEN_CONNS` at once, to leave connections for their callbacks.

### Metadata regeneration

Uploads mark metadata stale; npm packument denormalisation, Maven `maven-metadata.xml`, deb and rpm indexes are rebuilt by a bounded queue (`internal/worker/regen_queue.go`). Per key (npm package, Maven artifact, deb/rpm repository) there is at most one running and one pending regeneration, bursts coalesce after a 500 ms debounce (at most 5 s from the first upload), and `OPENARTIFACT_METADATA_REGEN_CONCURRENCY` caps concurrent runs across all formats. Runs take a Postgres advisory lock on a pinned connection; if another replica holds it the run is retried with backoff. On shutdown pending regenerations are drained for up to 10 s.

### Storage quota

Quota reservation is a single conditional upsert on the repository's `storage_quotas` row, committed with `synchronous_commit=off` so uploads do not queue behind each other's WAL flush. After a PostgreSQL crash the counters can lag the real usage by the last few hundred milliseconds of changes. The cleanup worker recounts every repository from its package rows at startup and then every 6 hours, which repairs such drift; see [keeping the counters correct](../repositories/cleanup-and-quotas.md#keeping-the-counters-correct).

### HTTP caching

The embedded SPA is gzip-precompressed at startup; hashed `assets/*` get `Cache-Control: public, max-age=31536000, immutable`, `index.html` gets `no-cache` with an ETag. Package metadata endpoints (maven-metadata.xml and checksums, deb Release/Packages, rpm repodata, helm `index.yaml`, PyPI simple, npm packument) send an ETag and answer `If-None-Match` with 304 after authorisation. Only anonymous reads of public repositories are `public`; authenticated responses are `private, no-cache` with `Vary: Authorization`.

### Rendered metadata cache and compression

Large generated documents are not rebuilt per request. The validators (ETags) of Helm `index.yaml`, conda `repodata.json`, the RubyGems specs indexes (`specs.4.8.gz`, `latest_specs.4.8.gz`, `prerelease_specs.4.8.gz`) and the Cargo sparse index entry of a crate come from a cheap fingerprint of the live rows (a count plus an order-independent hash of their IDs, one narrow index range, no blob join). A matching `If-None-Match` is answered with 304 before any row is loaded. A miss renders once and the document is kept in a process-wide cache of at most 64 MiB (least recently used out, one render at a time per document); the same cache holds the npm packument and `maven-metadata.xml`, the checksums of the latter are cached and computed for the requested algorithm only, and a group's merged npm packument is cached under a digest of its members' documents. Documents above a quarter of the budget are rendered per request instead of cached. The cache key embeds the fingerprint, so a publish, delete, trash or yank produces a new key (quarantine only gates downloads, which are checked on every request and are never cached) and an old rendering can never be served; replicas keep independent caches and need no coordination.

Text metadata (the documents above, PyPI simple pages and JSON, NuGet service index, search and registration pages, npm version manifests, Cargo search) is gzip-encoded when the client sends `Accept-Encoding: gzip`, with `Vary: Accept-Encoding` and a weak ETag on the compressed form. Cached documents are compressed once. Blobs, artifacts and Range requests are never compressed. The RubyGems specs files are already gzip files and are sent as they are.

The NuGet registration index takes its ETag from the IDs and versions of the package's rows, which it lists anyway. It used to read every `.nupkg` of a package to list its dependency groups. They are now stored in `nuget_packages.dependency_groups` at push time. Packages pushed before the `nuget_dependency_groups` migration have no stored value; the server derives it from the package on first use and records it, so the first registration request after an upgrade still reads those packages once.

#### What invalidates a cached document

A cached document is served only while its validator is unchanged, so every change that alters a document must move its validator:

| Document | Validator | Moves on |
|---|---|---|
| Helm `index.yaml`, conda `repodata.json` (per platform) | count and ID hash of the live rows | publish, delete, trash, restore (chart and package rows are immutable) |
| RubyGems specs indexes | count and ID hash of the listed (not yanked) gems | publish, yank, unyank, delete, trash, restore |
| Cargo sparse index entry | count and hash of ID plus `yanked` of the crate's live versions | publish, yank, unyank, delete, trash, restore (a download does not) |
| NuGet registration index | repository, base URL, and ID and version of every live row | publish, delete, trash, restore |
| npm packument (full and abbreviated) | `npm_packages.updated_at` | publish, unpublish, dist-tag change, deprecation, manifest edit, trash, restore |
| `maven-metadata.xml` and checksums | SHA-256 of the metadata blob | regeneration that produces different bytes |
| npm group merge | SHA-256 of the member documents it merges | any change of a member's document |

The npm packument validator is bumped by the statements that write the data, not by their callers: creating, editing or deleting a version, setting or deleting a dist-tag, and trashing or restoring a version each update `npm_packages.updated_at` in the same statement or transaction, whichever code path runs them (upload handlers, trash, backup restore). `MarkPackumentStale` only schedules the `latest` denormalisation.

Quarantine and repository blocking never change a listing. They are applied when an artifact is downloaded (403) and, for a blocked proxy or group member, when the request is served, so they need no invalidation. A rendering that is missing data because a part of its source could not be read (a NuGet package whose nuspec was unreadable, a group member that failed) is sent once with `Cache-Control: no-store` and no validator and is not kept in the cache, so it cannot outlive the failure.

### Audit log paging

`GET /api/v1/admin/audit` avoids `COUNT(*)`: pass `cursor` (empty for the newest page) for keyset paging with `has_more` and `next_cursor`. In offset mode `total` is exact on small tables and on the last page, a planner estimate on large unfiltered tables, and a lower bound on large filtered ones; `total_exact` says which.

### Rate limit

With `OPENARTIFACT_RATE_LIMIT_RPS` above 0, each client IP gets a token bucket of that many requests per second (burst equal to the rate). Excess requests get `429` with `Retry-After: 1`. It applies to every route, including downloads, `/health` and `/metrics`. Behind a reverse proxy set `OPENARTIFACT_TRUSTED_PROXIES`, otherwise all clients share the proxy's IP.

### Storage

Blobs are streamed through the server by default. On S3 and GCS backends you can opt in to [presigned URLs](storage.md#presigned-urls) (`storage.presignDownloads`), which redirect blob downloads of at least `presignMinSize` straight to the object store and take large transfers off the server. S3 uploads use 16 MiB parts with 4 in parallel. Files above 5 GiB go through [multipart presigned uploads](storage.md#multipart-uploads). See [storage](storage.md).
