<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Design: format plugins and generic package tables

Status: implemented in 0.15 (issue [#332](https://github.com/openartifact/openartifact/issues/332), item 1). This page is the design of record: the schema contract, what became data and what stayed code, the upgrade path and the trade-offs.

Goal: adding a package format costs about ten files, and no union query, trigger, trash spec, recovery entry or exhaustiveness test has to be touched. Before 0.15 a format cost one table (or two), a branch in ~21 enumerations (listing, count, catalog, detail, usage, security, blob references, tiering, trash, quota, cleanup, snapshot/backup, promote, recovery journal, federation, search, ...), about 24 `quota_pending_*` functions with three triggers each, a `format_exhaustive_test.go` next to every per-format `switch`, and a hand-kept list per subsystem that silently went stale (the ansible format touched 120 files; `auditPackageKinds` forgot Nix, `finishRoutes` forgot apk's webhook publisher). The cure has two halves, delivered together:

1. **Generic package tables** (`packages`, `package_versions`, `package_files`, `package_tags`) hold the data of every format but docker. Everything that spans formats reads them once instead of unioning 22 tables.
2. **A format plugin** (`internal/format/plugin`) is the single registration a format makes: hosted routes, analysis hooks (dependencies, license, PURL, manifest), client setup, proxy and group hooks, lifecycle hooks and the storage descriptor. Every method of the interface is required, so a forgotten capability is a compile error. The exhaustiveness tests that only existed to find forgotten switch arms are deleted.

## Scope

| In | Out (kept dedicated) |
|---|---|
| ansible, apk, cargo, composer, conan, conda, deb, go, helm, ivy, maven, npm, nix, nuget, pacman, pypi, raw, rpm, rubygems, sbt, terraform | **docker** |

**Why docker stays dedicated.** Docker is an object graph, not a package list. Manifests, layer blobs, referrers (`subject_digest`), upload sessions, the GC advisory lock (`docker-repo:<id>`), cross-repository mounts and a hand-written federation codec all depend on the dedicated `docker_*` tables, and the listing unit is a tag, not a file. Moving it is a separate, larger change. Docker is the **one explicit special case** in each cross-format subsystem (listing, usage, blob references, trash, quota, tiering, cleanup, search, recovery, backup, promote, federation), selected by the format name `docker`, never by a descriptor.

## Generic tables

The tables are created by `migrations/001_baseline.sql` (see [Baseline and upgrade](#baseline-and-upgrade)). All of them are journaled by the recovery journal and have the `update_updated_at` trigger. Names, types and indexes below are the contract every typed store and every subsystem codes against.

```sql
-- One row per logical package of a repository (maven group:artifact, npm @scope/name, deb name, ...).
CREATE TABLE packages (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    format        varchar(16) NOT NULL,           -- = repositories.format, denormalised for partial indexes
    name          text        NOT NULL,           -- listing/display name (what the UI and search show)
    name_key      text        NOT NULL,           -- identity inside the repository (normalised name); = name when the format does not normalise
    attrs         jsonb       NOT NULL DEFAULT '{}',  -- identity parts and package-level facts (maven group_id/artifact_id, npm scope, apk branch/repo, terraform kind)
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_packages_name ON packages (repository_id, name_key);
CREATE INDEX idx_packages_name_trgm ON packages USING gin (name gin_trgm_ops);

-- One row per version incarnation. Soft-deleted with its files (see Lifecycle).
CREATE TABLE package_versions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id    uuid        NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    repository_id uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    version       text        NOT NULL,           -- display version
    version_key   text        NOT NULL,           -- identity inside the package: = version, or version plus the coordinates the format keys on
                                                  -- (deb/rpm/conda: arch, release, build, component, ...). Also the text cleanup policies rank by.
    metadata      jsonb       NOT NULL DEFAULT '{}',  -- the format's version-level facts (cargo description/keywords/yanked, npm manifest, ...)
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    deleted_by    text                            -- the principal name (user, token, federation:<peer>)
);
CREATE UNIQUE INDEX uq_package_versions_live ON package_versions (package_id, version_key) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_pkg ON package_versions (package_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_repo ON package_versions (repository_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_deleted ON package_versions (package_id) WHERE deleted_at IS NOT NULL;

-- One row per stored file. The unit of listing, trash, quota, blob references and scan ids.
CREATE TABLE package_files (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),   -- the artifact id
    version_id     uuid        NOT NULL REFERENCES package_versions(id) ON DELETE CASCADE,
    repository_id  uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    path           text        NOT NULL,          -- repository-wide locator, unique while live; also the tiering path_glob subject
    filename       text        NOT NULL,          -- what a client downloads
    blob_id        uuid        REFERENCES blobs(id) ON DELETE RESTRICT, -- NULL only for files that have no blob (a go.mod file carries its text in attrs)
    size_bytes     bigint,                         -- declared size; wins over the blob's when set (rpm, rubygems)
    role           text        NOT NULL DEFAULT 'primary',  -- 'primary' counts as one artifact for quota; any other role (go mod, signature) counts 0
    listed         boolean     NOT NULL DEFAULT true,       -- true: a unit of the repository listing
    arch           text        NOT NULL DEFAULT '',  -- platform qualifier shown by the listing (arch, platform, python tag, os_arch, scala version, classifier)
    part           text        NOT NULL DEFAULT '',  -- extra retention-partition suffix (deb distribution, maven snapshot flag); '' for most formats
    attrs          jsonb       NOT NULL DEFAULT '{}',  -- file-level facts: listing extras (release, epoch, build_time, installed_size), per-format keys
    checksums      jsonb       NOT NULL DEFAULT '{}',  -- {"sha1":..,"md5":..,"shasum":..,"integrity":..} for checksum search; sha256 is the blob's
    unit_key       text,                           -- federation unit key when the unit is the file (or the version's primary file)
    download_count bigint      NOT NULL DEFAULT 0,  -- journal Exclude; federation ignores it
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,
    deleted_by     text
);
CREATE UNIQUE INDEX uq_package_files_path       ON package_files (repository_id, path) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_listing          ON package_files (repository_id, created_at DESC, id) WHERE deleted_at IS NULL AND listed;
CREATE INDEX idx_package_files_version          ON package_files (version_id);
CREATE INDEX idx_package_files_filename         ON package_files (repository_id, filename) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_blob             ON package_files (blob_id) WHERE blob_id IS NOT NULL;   -- full: a trashed row still pins its blob
CREATE INDEX idx_package_files_trash            ON package_files (repository_id, deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_package_files_sha1             ON package_files ((checksums->>'sha1'))   WHERE checksums ? 'sha1'   AND deleted_at IS NULL;  -- likewise md5 and shasum
CREATE INDEX idx_package_files_unit             ON package_files (repository_id, unit_key) WHERE unit_key IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_package_files_path_prefix      ON package_files (repository_id, path text_pattern_ops) WHERE deleted_at IS NULL AND path IS NOT NULL;  -- raw prefix listing

-- Mutable named pointers to versions (npm dist-tags; the next format with channels).
CREATE TABLE package_tags (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id uuid        NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    tag        text        NOT NULL,
    version    text        NOT NULL,   -- a version string, resolved at read time (a tag may name a trashed version)
    unit_key   text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (package_id, tag)
);
CREATE INDEX idx_package_tags_unit ON package_tags (unit_key) WHERE unit_key IS NOT NULL;
```

Design rules that follow:

- **Plugins compute, subsystems read.** Everything a cross-format subsystem needs as a per-format expression (listing name, filename, size rule, arch, tiering path, search name, retention partition, federation unit key) is a *column written by the store at publish time*, not a `CASE format` at read time. The subsystems therefore contain no format names.
- **`package_files.id` is the artifact id.** Listing ids, `GET/DELETE /packages/{id}`, `TrashRef{Kind,ID}`, `artifact_refs.artifact_path`, `vex_statements.artifact_id`, `release_bundle_items`, `audit_logs.resource_id`, recovery row keys and the scan-by-id results all name a `package_files` row. For formats whose unit is a version (cargo, helm, nuget, npm, go, ...) the version's primary file carries the id; a go module's `.mod` file is an extra file with `listed = false`.
- **Two live-unique keys replace the 24 per-format ones:** `(package_id, version_key)` on versions and `(repository_id, path)` on files. The store renders the natural key of the format into `version_key`/`path` (rpm: `version-release:epoch|arch` / `name-version-release.arch.rpm`; conda: `version-build|platform` / `platform/filename`; conan: `version@user_channel#rrev` / `name/version@user_channel#rrev/[<package_id>#<prev>/]<filename>`; maven: `version[#snapshot_ts-build]` / the repository path). Soft-deleted duplicates stay legal because both indexes are partial. A violation on write is `database.ErrDuplicate` (message contains "already exists"); a violation on restore is `ErrTrashConflict`.
- **A version is live iff it has a live file.** Trashing files stamps the same `deleted_at`/`deleted_by` on the file and, when none is left live, on the version; restoring a file re-attaches it to the live version of the same `version_key` when there is one (merge), otherwise revives its own. A purge of the last file deletes the version, and of the last version the package.
- **Counting.** A repository's artifact count is the number of live-or-trashed `package_files` with `role = 'primary'`; bytes are the sum of the blob sizes of all files. Soft delete releases nothing; hard delete and purge do, through the `quota_pending` protocol (the baseline triggers on `package_files` only consume or queue reservation entries, matched by exact `(size, count)`). A store reserves **per file**: a go module reserves `(zip size, 1)` and `(go.mod size, 0)`.
- **`size_bytes`** is NULL unless the format's own size must win over the blob's (rpm `package_size`, rubygems `size`); the listing computes `COALESCE(size_bytes, blob.size_bytes, 0)`.
- **Volatile state is excluded from the journal and federation** by naming it: `download_count` (journal `Exclude`, never replicated) and npm's `packages.attrs` keys `latest_version` and `packument_stale` (never replicated; the journal records `packument_stale` because it can only exclude whole columns).

### Side tables and the dedicated tables that stay (each justified)

A side table extends `package_files` (`id uuid PRIMARY KEY REFERENCES package_files ON DELETE CASCADE`; the column is called `id` because the journal trigger keys on it). It holds only what cannot ride in `attrs`/`metadata`: an FK, an index the format's own queries need, or bulk data that must stay out of the narrow listing row. It is journaled with its parent first, backed up and promoted with it, and read only by the format's own store. Nothing cross-format reads a side table.

| Table | Format | Why it stays separate |
|---|---|---|
| `conan_files (id, package_id, package_revision)` | conan | Binary packages are addressed by an 8-column key including `package_id` and `package_revision`; the lookups need typed columns. |
| `deb_files (id, distribution_id, component_id, control_fields)` | deb | FKs to the distribution graph, the per-distribution cleanup partition and metadata generation; `control_fields` is bulk (journal-excluded). |
| `maven_files (id, classifier, extension, snapshot_timestamp, snapshot_build_number, is_snapshot, md5, sha1, sha512)` | maven | Snapshot ordering, `maven-metadata.xml` generation and the newest-snapshot retention protection need typed, indexed coordinates and digests. |
| `pacman_files (id, file_list, signature, signature_source, signature_key)` | pacman | `file_list` NULL means "not read from the package yet" and drives a worker anti-join; it is large and derivable, so the journal excludes it (and `signature`). |
| `rpm_files (id, header_start, header_end, rpm_extra)` | rpm | `rpm_extra` (file list, changelog, dependencies) is megabytes per row and read only by repodata generation; copied as-is, never re-parsed (re-parsing would re-read every package blob). Journal-excluded. |
| `debian_distributions`, `debian_components`, `debian_architectures` | deb | The distribution graph the `Release` files are generated from; repository-scoped state, not package rows. Companions of the deb backup descriptor. |
| `rpm_repos` | rpm | Per-repository repodata state (`metadata_stale`, base arch). |
| `apk_signing_keys`, `nix_signing_keys` | apk, nix | Per-repository signing secrets; never package data. |
| `nix_pending_nars` | nix | Staging rows of NARs awaiting their narinfo; they pin blobs but are not packages. |
| `group_metadata_state`, `group_package_owners` | group repos | Repository-level state of generated group metadata; no package rows. |
| `docker_blobs`, `docker_blob_images`, `docker_manifests`, `docker_manifest_refs`, `docker_tags`, `docker_uploads` | docker | See [Scope](#scope). |

### Per-format mapping

`P` = `packages`, `V` = `package_versions`, `F` = `package_files`. The exact column-to-JSON mapping of each format is the doc comment of its `internal/database/pkg_<format>.go`; this table is the overview.

| Format | Grain | P name (`name_key`) | V `version_key` | F path / arch / notes |
|---|---|---|---|---|
| ansible | 1 V, 1 F | `namespace.name` | version | filename; deps and `requires_ansible` in `V.metadata` |
| apk | 1 V, 1 F | `branch/repo/name` | `version\|arch` | `branch/repo/arch/name-version.apk`, arch |
| cargo | 1 V, 1 F | name (`name_key` = `lower(name)`) | version | `name-version.crate`; `yanked` in `V.metadata`; unit key `name@version` |
| composer | 1 V, 1 F | `vendor/package` | `version_normalized` | synthesised zip name; `composer_json` in `V.metadata` |
| conan | V per recipe revision, F per file | name | `version@user_channel#rrev` | roles by file; only the recipe manifest (`''`/`conanmanifest.txt`) is `listed`, its `filename` is the full reference (the real name is `attrs.filename`); `conan_files` |
| conda | V per build and platform, F per extension | name | `version-build\|platform` | `platform/filename`, arch = platform; `depends` kept in order |
| deb | 1 V, 1 F | name | `version\|arch\|<component id>` | pool path, arch, part = distribution id; `deb_files` |
| go | one V; F = zip (primary, listed) + `.mod` (role `mod`, unlisted) | module path | version | `module/@v/version.zip`; `go_mod_content` in the `.mod` row's `attrs`; unit key `module@version` on the zip |
| helm | 1 V, 1 F | name | version | `name-version.tgz`; unit key `name@version` |
| ivy | V per revision, F per artifact | `organisation:module` | revision | `organisation/module/revision/artifact` |
| maven | V per version (a snapshot build is its own V), F per file | `group:artifact`, `attrs{group_id,artifact_id}` | `version[#snapshot_ts-build]` | the repository path (= unit key); arch = classifier; part = `true:<version>` for snapshots, `false` for releases; `maven_files` |
| npm | P per package, V per version, F = tarball | `@scope/name` | version | `full_name/-/filename`; unit key `full_name@version`; `checksums{integrity,shasum}`; manifest in `V.metadata`; dist-tags in `package_tags` (unit key `full_name#tag`); `packument_stale`, `latest_version` in `P.attrs` |
| nix | 1 V, 1 F | name | store hash | `<hash>.narinfo` |
| nuget | 1 V, 1 F | package id (`name_key` = `lower(id)`) | version | `id.version.nupkg`; unit key `package_id@version` |
| pacman | 1 V, 1 F | name | `version\|arch` | filename, arch; `pacman_files`; md5 in `F.attrs` |
| pypi | V per release, F per distribution file | normalised name | version | filename; arch = python tag; unit key `name/version/filename` |
| raw | one P+V+F per path | the path | `''` | the path (= unit key) |
| rpm | 1 V, 1 F | name | `version-release:epoch\|arch` | `name-version-release.arch.rpm` (prefixed `<epoch>:` when non-zero), arch; `size_bytes = package_size`; `rpm_files` |
| rubygems | 1 V, 1 F | name | `version[-platform]` | `name-version[-platform].gem`, arch = platform; `size_bytes = size` |
| sbt | V per version, F per file | `group:artifact` | version | filename, arch = scala version |
| terraform | modules 1:1; providers V per version, F per os/arch | `modules/name/system`, `providers/type` (`attrs.kind`) | version | module archive / provider zip; arch = `os_arch` |

## Cross-format subsystems on the generic tables

| Subsystem | Before | After |
|---|---|---|
| Listing and count: `ListRepoComponents`, `…ByName`, `CountRepoComponents`, `GetRepoComponentBlob`, `ResolveComponentNames` | 22 branches each, two-stage per `package-listing.md` | **One generic branch** over `package_files` (listed, live) plus two pseudo-branches: docker tags and proxy-cache entries. Stage 1 takes the top `limit+offset` files by `(created_at DESC, id)` from `idx_package_files_listing`, joined to V and P only when `format_filter`, `name_filter` or `q` is set; stage 2 resolves name, version and blobs for the page by primary key. `ResolveComponentNames` has no deleted/listed filter, so trashed rows still resolve. |
| Catalog roll-up, `catalog_detail`, `repo_usage`, findings name matching | per-table branches | One branch over F/V/P. The artifact detail is a **data descriptor** (`storage.Detail`: where each display field lives in `p`, `v`, `f`, the checksums, the side table or a sibling file, and the name of a dependency decoder in `internal/api`), resolved by one pure function; no per-format Go decoding switch. |
| Blob references (`blobRefSpecs`), tiering | 26 entries | `package_files.blob_id` plus the docker and side tables that own a blob column. Tiering `path_glob` matches `package_files.path`. |
| Trash (24 specs), restore, purge, list | per-kind table specs | Generic file-scope operations on `package_files` with version stamping. `storage.Trash` carries the per-format kind string, scope (`ScopeFile`, or `ScopeVersion` for go: the zip drags the `.mod` along), name/version display expressions, regeneration keys and the npm hooks. Docker has its own path (`trash_docker.go`). |
| Quota | 24 functions × 3 triggers | One function set on `package_files` (and `docker_blobs`), `role = 'primary'` counting one artifact |
| Retention cleanup | `formatTable` per format | Generic `DENSE_RANK` over `(partition, version expression)` of `storage.Cleanup`; protection (`ProtectExpr`) is a compile-time SQL fragment (npm tagged versions, newest maven snapshot build); deletion goes through the generic trash/hard-delete path. |
| Search and checksum search | 23-branch `UNION ALL` | One query over F/V/P (+ docker tags); sha256 by blob, sha1/md5 over the `checksums` expression indexes. |
| Promote | 17 `rowSpec`s + 6 bespoke promoters | **One** promoter copying P/V/F, the side-table rows and npm tags, parameterised by `storage.Promotion`. |
| Backup | `exportFormatTables` with 22 cases, 39 restore functions | One `package_rows/<repo>/<table>.jsonl` set for `packages`, `package_versions`, `package_files`, `package_tags` and the descriptor's side tables and companions. Archive version **2.0**. |
| Recovery journal | 39 table entries and triggers | Registry built by `recovery.NewRegistry()` from the generic tables, the `SideTables` of the registered descriptors and the docker tables; sidecar magic **2**. |
| Federation | 11 capture triggers, 9 per-format codecs | Capture triggers on `package_files`, `package_versions` (update) and `package_tags`; one generic codec parameterised by `storage.Federation`; docker keeps its codec. **Protocol 2.** |

## The plugin

`internal/format/plugin` defines the interface and the registry; `internal/formats` lists the registrations (one file per format, one line each in `registrations()`, no `init()`). The plugin layer sits **above** the libraries: `proxy`, `group`, `vuln`, `sbom`, `pkgdeps`, `licensemeta`, `presign`, ... never import it. `formats.Register()` *pushes* each registration into their leaf registries (`proxy.Register`, the group, analysis and lifecycle hooks, `storage.Register`), so a library asks its own registry, not the plugin. Every binary and test binary that needs the hooks calls `formats.Register()` (idempotent) from `main`/`TestMain`. `internal/formats` must not import `internal/api`, `internal/worker` or `internal/middleware`.

```go
// Format is the one registration a package format makes. Every method is required:
// a format without a capability returns the explicit zero value (plugin.NoProxy,
// plugin.NoGroup, plugin.NoAnalysis, lifecycle.None), never nil.
type Format interface {
    Name() string                          // = repotype.Spec.Name = repositories.format
    Hosted(*Deps) (Hosted, error)          // the hosted-repository handler and its routes
    Analysis() Analysis                    // Manifest, Deps, License, Vuln, SBOM hooks
    ClientSetup() clientsetup.Builder      // "Use this repository" snippets
    Proxy() Proxy                          // NoProxy or ProxyOf(hooks)
    Group() Group                          // NoGroup, FirstHitGroup or GroupOf(hooks)
    Lifecycle() lifecycle.Hooks            // Stale, Removed, Restored: metadata refresh outside the handler
    Storage() storage.Storage              // how the generic engines treat the format's rows
}
```

`plugin.Definition{ID, NewHosted, Supply, Snippets, Mirror, Merge, Meta, Store}` is the table form most formats use. Optional capabilities of the hosted handler are discovered by interface (`Guarded`, `APIRouted`, `PublicRouted`, `WriteRouted`, `PutRouted`). Where the routes mount is `repotype.Spec.Mount` (`Shared`, `Guarded`, `Isolated`, `Dispatcher`, `OCI`), and registration order is the precedence of the shared `/repos/{name}` subrouter (`TestSharedRouteClashesAreKnown` fails on a new route-shape clash). `TestRegistrationsMatchSpecs` asserts that the registered set equals `repotype.Names()` and that `Spec.Proxy`/`Spec.Group`/`Spec.Federation` agree with the registrations.

### The storage descriptor

`internal/format/storage` is a leaf package (standard library and `internal/registry` only), so the database, workers, search, promote, federation, backup and recovery all read it without a cycle. The zero value of every field is the generic default; a format states only what differs.

| Field | What it says | Values that differ from the default |
|---|---|---|
| `Trash{Kind, Scope, NameExpr, VersionExpr, RegenKeys, OnTrash, OnRestore, OnPurge}` | the API trash kind (one per format), unit scope, display expressions over `f`, `v`, `p`, the regeneration keys, hooks | `Scope: ScopeVersion` for go; npm hooks (`UPDATE packages SET updated_at = now()` on trash, restore and purge: the packument ETag; delete the `package_tags` of a purged version); maven `RegenKeys` = group id and artifact id; conda/rpm/deb/conan/raw `VersionExpr`; rubygems, apk, terraform, ivy, sbt, pypi, raw, conda `NameExpr` |
| `Cleanup{Partition, VersionExpr, ProtectExpr}` | what counts as "the same package" and what is protected | default partition `p.id, f.arch, f.part`; maven `p.id, f.part` and the newest-snapshot protection; npm protects tagged versions; pypi `p.id`; raw the path directory; conan ranks `split_part(version_key,'#',1)` (revisions are not versions); apk/deb/pacman/rpm/rubygems rank `split_part(version_key,'\|',1)`; conda strips the platform |
| `Federation{Unit, Tags, TouchPackage, StaleAttr, LocalPackageAttrs}` | nil = not federated | cargo, helm, nuget, go: `UnitVersion`; raw, pypi, maven: `UnitFile`; npm: `UnitVersion` + `Tags` + `TouchPackage` + `StaleAttr: packument_stale` + `LocalPackageAttrs: latest_version, packument_stale` |
| `Detail{Summary, Description, License, Homepage, MD5, SHA1, Info[], Deps, Decoder}` | where the artifact detail page reads each field (`p`, `v`, `f`, `c`hecksums, `s`ide table, `k` columns, `m` sibling file) | data only; the dependency `Decoder` is a name in `internal/api` (`npm-manifest`, `cargo-metadata`, `go-mod`, ...) |
| `Promotion{WholeVersion, SharedVersion, Tags, Regen, Licenses, PackageAttrs, VersionKey, Part, Collide, Prepare, AfterCopy, SideSet}` | how the single promoter copies rows | go, conan `WholeVersion`; conan, maven, ivy, sbt, terraform, pypi `SharedVersion`; npm `Tags` + `AfterCopy` (latest tag, packument stale); deb `Prepare` (destination distribution, component, architecture) + `VersionKey` + `Part` + `SideSet` + `Collide`; rpm `Collide` + `AfterCopy` (stale flag); `Licenses` for cargo, composer, pypi, rpm, apk, pacman, rubygems |
| `Backup{Companions, Regen}` | tables a restore must bring first, and generated metadata to regenerate | deb: the `debian_*` companions and `RegenDeb`; `Regen` for rpm, apk, maven, npm, pacman |
| `SideTables []SideTable{Name, Exclude, Refs}` | side tables of the format, parents first | conan `conan_files`; deb `deb_files` (exclude `control_fields`, refs distribution and component); maven `maven_files`; pacman `pacman_files` (exclude `file_list`); rpm `rpm_files` (exclude `rpm_extra`) |

A format that is not registered has no descriptor, and the generic engines reject it (`ErrTrashUnknownKind`, `ErrNoStorageDescriptor`, `ErrUnsupportedFormat`) instead of guessing.

### What becomes data and what stays code

| Decision point (every place that switched on the format) | After |
|---|---|
| Router mounts, `SetPublisher` list, REST upload routes, upload-metrics routes | a loop over the registry; placement from `Spec.Mount`; guards and extra routes are optional capabilities of the hosted value |
| proxy `formatServer`, `Immutable`, metadata/blob predicates, presign blob paths | `Proxy` hooks, registered into the proxy package |
| group `serveMerged`, `packageRef`, hosted-wins package probes | `Group` hooks; the probes read `packages` |
| worker metadata refresh after stale/restore/removal, gpg key refresh | `Lifecycle` |
| `pkgdeps`, `licensemeta`, `vuln` PURL/Trivy, `sbom` PURL, `ai/manifest` | `Analysis` |
| `clientsetup.builderFor` | `ClientSetup()` |
| artifact detail decoders | `storage.Detail` (data) + named decoders |
| trash kinds and specs, cleanup tables, federation codecs, promotion rows, backup restore functions, recovery table list | `storage.Storage` |
| listing, usage, quota, tiering, search, blob references | one generic query each; no format names |

Tests that only pinned one of those switches are deleted (`format_exhaustive_test.go`, `TestPackageTablesInUnions`, the legacy listing-equivalence oracle, ...). Tests that guard a real per-format *decision* stay (`TestRegistrationsMatchSpecs`, `TestTrashKindsComeFromDescriptors`, `TestCleanupFormatSupported`, `TestGroupMetadataCoversEveryFormat`).

### Remaining explicit switches, and why

These stay code because their branches run genuinely different code, or because a dependency direction forbids data:

- **Docker, once per engine** (listing, usage, blob references, trash, quota, tiering, cleanup, search, backup, promote, recovery, federation): the dedicated tables.
- `cmd/server` `restUploadHandlers()`: the REST upload handlers live in `internal/api`, which `internal/formats` must not import; the map is keyed by format name.
- `cmd/server/app_scan.go`: builds the docker handler directly for worker-only processes.
- `repodispatch`: conan's read-only session check.
- Worker `group_metadata.go`: the merged-metadata builder per format (deb, rpm, pacman, apk).
- Metadata regeneration after restore or promotion switches on `storage.RegenKind` (`deb`, `rpm`, `apk`, `maven`, `npm`, `pacman`): the regenerators are different code.
- `cmd/server/federation.go` `regenNotifier`: maven and npm notifications need format-specific keys.
- `api/vuln_distro.go` (deb/rpm distribution detection) and `server.go` `quarantineKeys` (handler path spellings).
- Rebuild's npm packument loop.

Everything else a new format adds is a table row in one descriptor.

## Baseline and upgrade

**There is no in-place data migration.** 0.15 ships one squashed `migrations/001_baseline.sql` that replaces migrations 001-099 of the 0.x line and creates the generic tables directly. This is deliberate: an expand/backfill/switch/contract migration of every package table (mirror triggers on 25 old tables, batched backfills, one more release before the old tables can be dropped) was weighed against a clean schema and rejected, because the data can be reproduced exactly from what the registry already serves, and the migration would have kept two schemas alive for a release.

- **Upgrade path.** Start 0.15 on an **empty** database and copy the data from a running 0.14 server with `oa import openartifact` (REST download then the normal upload of each format, so every row is rebuilt through the new stores). Runbook: [docs/migration/openartifact.md](../migration/openartifact.md#runbook-014-to-015). The user-visible procedure and behaviour changes are in [upgrading](../getting-started/upgrading.md#0150).
- **A 0.15 binary on a 0.14 database refuses to start** with `database.ErrLegacySchema`: `schema_migrations` holds migrations 0.15 does not ship and not `001_baseline.sql`. The message names `oa import`.
- **Backup archives of 0.14 are refused** (archive version is now `2.0`; the error points to `oa import`). **Recovery sidecars of 0.14 are refused** (`SidecarMagic` is 2; `recovery.ErrSidecarLegacy`, reported in `Report.SidecarsLegacy`; a rebuild that finds only legacy sidecars fails). **Federation protocol 2** refuses a protocol-1 peer ("upgrade the peer to 0.15 or later"); upgrade all members.
- **Ids are not preserved.** A file gets a fresh `package_files.id`. Restore re-derives ids as `md5(repoID||oldID)::uuid`. Scan results, VEX decisions, properties and audit history are not carried by `oa import` (see the runbook's "what is not reachable").
- **Future migrations start at `002_<topic>.sql`.** The baseline widening `chk_repo_format` is the only schema work a new format needs (`002_<format>.sql` plus `002_<format>_validate.no_tx.sql`).

## Adding a format

About ten files; the full checklist and a worked touch list are in [docs/development/README.md](../development/README.md#adding-a-format). In short: a `repotype.Spec` entry (and its upload routes), a `migrations/NNN_<format>.sql` widening `chk_repo_format`, the handler package `internal/format/<format>/`, the typed store `internal/database/pkg_<format>.go` with its queries, `internal/formats/<format>.go` with one line in `registrations()` (carrying the storage descriptor), the client-setup builder, the web label/colour/icon, `scripts/smoke/<format>.sh` and `docs/formats/<format>.md`. No table, union branch, trigger, trash/quota/cleanup/backup/promote/recovery entry or exhaustiveness test.

## Trade-offs and risks

- **Docker stays dedicated.** The cost is one explicit docker branch in every engine; the benefit is that the OCI object graph is untouched. Folding it in is a separate change.
- **Per-format semantics live in jsonb.** `attrs`, `metadata` and `checksums` are not typed columns; a store's doc comment is the schema of its format, and the round-trip test of each store pins it. Hot predicates are lifted into real columns or expression indexes (`listed`, `role`, `arch`, `part`, `unit_key`, the sha1/md5/shasum indexes). sha512 and npm `integrity` have no index and are not searchable.
- **Listing is one branch.** Measured on 1.05 M files (600 k in one npm repository): first page 2.4 ms, offset 100 000 133 ms, `name_filter` 77 ms, count 62 ms. Inherently full-scan cases remain: a rare `q` (4.7-5.9 s on 600 k files, `strpos` cannot be estimated and `version` has no trigram index), a `format_filter` that matches nothing, and the by-name sort. The two-stage shape and `idx_package_files_listing` are kept per `.agents/rules/package-listing.md`.
- **Trash kinds are per format, derived from descriptors.** Terraform providers list under the kind `terraform_module`; a trash kind needs a registered descriptor, a docker kind or `repository`, otherwise `ErrTrashUnknownKind`.
- **Federation `Enable` is heavier.** It locks `package_files`/`package_versions` (+ `package_tags` for npm) in SHARE mode for the backfill, which blocks package writers of all repositories during it.
- **Intentional behaviour changes** are listed with the release notes: [upgrading](../getting-started/upgrading.md#0150).
