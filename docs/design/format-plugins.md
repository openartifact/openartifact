<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Design: format plugins and generic package tables

Status: proposed (issue [#332](https://github.com/openartifact/openartifact/issues/332), item 1). This document is committed before the code and is updated with the decisions the implementation forced; the last section lists what was deferred.

Goal: adding a package format costs about ten files, and no union query, trigger, trash spec or recovery entry has to be touched. Today a format costs one table (or two), a branch in ~21 enumerations (listing, count, catalog, detail, usage, security, blob references, tiering, trash, quota, cleanup, snapshot/backup, promote, recovery journal, federation, search, ...), about 24 `quota_pending_*` functions with three triggers each, a `format_exhaustive_test.go` next to every per-format `switch`, and a hand-kept list per subsystem that silently goes stale (the ansible format touched 120 files; `auditPackageKinds` forgot Nix, `finishRoutes` forgot apk's webhook publisher). The cure has two halves that this change delivers together:

1. **Generic package tables** (`packages`, `package_versions`, `package_files`, `package_tags`) hold the data of every format but docker. Everything that spans formats reads them once instead of unioning 22 tables.
2. **A format plugin** (`internal/format/plugin`) is the single registration a format makes: spec, hosted routes, proxy and group hooks, analysis hooks (dependencies, license, PURL, manifest), client setup, lifecycle hooks. Every method of the interface is required, so a forgotten capability is a compile error; the exhaustiveness tests that existed only to find forgotten switch arms are deleted.

## Scope

| In | Out (kept dedicated, with the reason) |
|---|---|
| ansible, apk, cargo, composer, conan, conda, deb, gomod, helm, ivy, maven, npm, nix, nuget, pacman, pypi, raw, rpm, rubygems, sbt, terraform | **docker**: an object graph, not a package list. Manifests, layer blobs, referrers (`subject_digest`), upload sessions, the GC advisory lock (`docker-repo:<id>`), cross-repository mounts and a hand-written federation codec all depend on the dedicated tables, and the listing unit is a tag, not a file. Moving it is a separate, larger change. Docker keeps its `docker_*` tables and stays the one remaining dedicated branch in each cross-format subsystem (listing, usage, blob references, trash, quota, tiering, cleanup, search, recovery, federation); the plugin interface has an explicit `Dedicated` storage declaration so the next format that does not fit can do the same without a new `switch`. |

## Generic tables

All new tables are journaled (`internal/recovery/tables.go`) and have the `update_updated_at` trigger. Names, types and indexes below are the contract every format port and every subsystem codes against.

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
CREATE INDEX idx_packages_name_trgm ON packages USING gin (lower(name) gin_trgm_ops);

-- One row per version incarnation. Soft-deleted with its files (see Lifecycle).
CREATE TABLE package_versions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id    uuid        NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    repository_id uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    version       text        NOT NULL,           -- display version
    version_key   text        NOT NULL,           -- identity inside the package: = version, or version plus the coordinates the format keys on
                                                  -- (deb/rpm/conda: arch, release, build, component, ...). Also the text cleanup policies rank by.
    metadata      jsonb       NOT NULL DEFAULT '{}',  -- the format's version-level columns, as today's table columns (cargo description/keywords/yanked, npm manifest, ...)
    unit_key      text,                           -- federation unit key when the unit is the version (NULL: not federated or file-grain)
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    deleted_by    uuid
);
CREATE UNIQUE INDEX uq_package_versions_live ON package_versions (package_id, version_key) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_pkg ON package_versions (package_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_versions_repo ON package_versions (repository_id) WHERE deleted_at IS NULL;

-- One row per stored file. The unit of listing, trash, quota, blob references and scan ids.
CREATE TABLE package_files (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),   -- migrated rows keep the old row id (see Identifiers)
    version_id     uuid        NOT NULL REFERENCES package_versions(id) ON DELETE CASCADE,
    repository_id  uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    path           text        NOT NULL,          -- repository-wide locator, unique while live; also the tiering path_glob subject
    filename       text        NOT NULL,          -- what a client downloads
    blob_id        uuid        REFERENCES blobs(id) ON DELETE RESTRICT, -- NULL only for rows that never had a blob (legacy go/conan rows)
    size_bytes     bigint,                         -- declared size; wins over the blob's when set (rpm, rubygems)
    role           text        NOT NULL DEFAULT 'primary',  -- 'primary' counts as one artifact for quota; any other role (go mod/info, signature) counts 0
    listed         boolean     NOT NULL DEFAULT true,       -- true: a unit of the repository listing
    arch           text        NOT NULL DEFAULT '',  -- platform qualifier shown by the listing (arch, platform, python tag, os_arch, scala version, classifier)
    part           text        NOT NULL DEFAULT '',  -- extra retention-partition suffix (deb distribution, maven snapshot flag); '' for most formats
    attrs          jsonb       NOT NULL DEFAULT '{}',  -- file-level facts: listing extras (release, epoch, build_time, archive_size, installed_size), per-format keys
    checksums      jsonb       NOT NULL DEFAULT '{}',  -- {"sha1":..,"md5":..,"sha512":..,"shasum":..} for checksum search; sha256 is the blob's
    unit_key       text,                           -- federation unit key when the unit is the file
    download_count bigint      NOT NULL DEFAULT 0,  -- journal Exclude; federation ignores it
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,
    deleted_by     uuid
);
CREATE UNIQUE INDEX uq_package_files_path       ON package_files (repository_id, path) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_listing          ON package_files (repository_id, created_at DESC, id) WHERE deleted_at IS NULL AND listed;
CREATE INDEX idx_package_files_version          ON package_files (version_id);
CREATE INDEX idx_package_files_filename         ON package_files (repository_id, filename) WHERE deleted_at IS NULL;
CREATE INDEX idx_package_files_blob             ON package_files (blob_id) WHERE blob_id IS NOT NULL;   -- full: a trashed row still pins its blob
CREATE INDEX idx_package_files_trash            ON package_files (repository_id, deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_package_files_sha1             ON package_files ((checksums->>'sha1'))   WHERE checksums ? 'sha1'   AND deleted_at IS NULL;  -- likewise md5 and shasum
CREATE INDEX idx_package_files_unit             ON package_files (repository_id, unit_key) WHERE unit_key IS NOT NULL AND deleted_at IS NULL;

-- Mutable named pointers to versions (npm dist-tags; the next format with channels).
CREATE TABLE package_tags (
    package_id uuid NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    tag        text NOT NULL,
    version    text NOT NULL,   -- a version string, resolved at read time (a tag may name a trashed version)
    unit_key   text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (package_id, tag)
);
```

Design rules that follow from the above:

- **Plugins compute, subsystems read.** Everything a cross-format subsystem needs as a per-format expression today (listing name, filename, size rule, arch, tiering path, search name, retention partition, federation unit key) is a *column written by the plugin at publish time*, not a `CASE format` at read time. The subsystems therefore contain no format names.
- **`package_files.id` is the unit id.** Listing ids, `GET/DELETE /packages/{id}`, `TrashRef{Kind,ID}`, `artifact_refs.artifact_path`, `vex_statements.artifact_id`, `release_bundle_items`, `audit_logs.resource_id`, recovery row keys and the scan-by-id results all name today's row id. Migrated files keep it. For formats whose old unit was a version (cargo, helm, nuget, npm, ...) the version's primary file takes the old id; gomod's `.mod`/`.info` become extra files with new ids and `listed = false`.
- **`package_versions.id` = the old id too** where one old row was one version (it lives in another table, so no clash); otherwise a deterministic `md5(package_id|version_key|<deleted_at or 'live'>)::uuid`, which makes the backfill idempotent and lets live siblings of one version share a row.
- **Two live-unique keys replace the 24 per-format ones:** `(package_id, version_key)` on versions and `(repository_id, path)` on files. The plugin renders the old natural key into `version_key`/`path` (rpm: `version-release:epoch` / `name-version-release.arch.rpm`; conda: `version-build` / `platform/filename`; conan: `version@user_channel#rrev` / `<package_id>/<prev>/<filename>`; maven: `version` / the repository path). Soft-deleted duplicates stay legal because both indexes are partial.
- **Version state follows its files.** A version is live iff at least one of its files is live. Trashing files stamps the same `deleted_at`/`deleted_by` on the file and, when none is left live, on the version; restoring a file re-attaches it to the live version of the same `version_key` when there is one (merge), otherwise revives its own. A unique violation on `uq_package_files_path` or `uq_package_versions_live` is `ErrTrashConflict`, as today.
- **Counting.** A repository's artifact count is the number of live-or-trashed `package_files` with `role = 'primary'`; bytes are the sum of the blob sizes of all files. Soft delete releases nothing; hard delete and purge do (the existing `quota_pending` protocol, now on one table).
- **`size_bytes`** is NULL unless the format's own size must win over the blob's (rpm `package_size`, rubygems `size`); the listing computes `COALESCE(size_bytes, blob.size_bytes, 0)`.

### Kept side tables (each justified)

A side table is keyed by `package_files.id` (`file_id uuid PRIMARY KEY REFERENCES package_files ON DELETE CASCADE`) unless noted, holds only what cannot ride in `attrs`/`metadata` (an FK, an index the format's metadata generation needs, or bulk data that must stay out of the narrow listing row), is journaled with its parent first, and is read only by the format's own code. Nothing cross-format reads a side table.

| Table | Format | Why it stays separate |
|---|---|---|
| `debian_distributions`, `debian_components`, `debian_architectures` | deb | The distribution graph the `Release` files are generated from; not package rows. Unchanged. |
| `deb_files (file_id, distribution_id, component_id, control_fields jsonb)` | deb | FKs to the graph, the `ListDebPackageEntries` join and the per-distribution cleanup partition; `control_fields` is bulk. |
| `rpm_repos` | rpm | Per-repository repodata state (`metadata_stale`, base arch). Unchanged. |
| `rpm_files (file_id, header_start, header_end, rpm_extra jsonb)` | rpm | `rpm_extra` (file list, changelog, dependencies) is megabytes per row and read only by repodata generation; migrated as-is, never re-parsed (re-parsing would force a re-read of every package blob). |
| `pacman_files (file_id, file_list text NULL)` | pacman | NULL means "not read from the package yet" and drives a worker anti-join; excluded from the journal (large, derivable). |
| `apk_files (file_id, ...)` | apk | The APKINDEX stanza fields, bulk; the package-level `branch/repo` ride in the package name. |
| `conan_files (file_id, package_id, package_revision)` | conan | Binary packages are addressed by the 8-column key including `package_id` and `package_revision`; the lookups need an index on them. |
| `maven_files (file_id, classifier, extension, is_snapshot, snapshot_timestamp, snapshot_build_number, md5, sha1, sha512)` | maven | Snapshot ordering, `maven-metadata.xml` generation and the newest-snapshot retention protection need typed, indexed coordinates. |
| `nix_pending_nars`, `nix_signing_keys`, `apk_signing_keys` | nix, apk | Staging rows and secrets; never package data. Unchanged. |
| `group_metadata_state`, `group_package_owners` | group repos | Repository-level, no package rows. Unchanged. |
| `docker_blobs`, `docker_blob_images`, `docker_manifests`, `docker_manifest_refs`, `docker_tags`, `docker_uploads` | docker | See Scope. |

### Per-format mapping

`P` = `packages`, `V` = `package_versions`, `F` = `package_files`. "1:1" = one old row becomes one V and one F that both take the old id.

| Format | Old table(s) | Grain | P name | V version_key | F path / arch / notes |
|---|---|---|---|---|---|
| ansible | `ansible_collections` | 1:1 | `namespace.name` | version | filename; deps and `requires_ansible` in `V.metadata` |
| apk | `apk_packages` | 1:1 | `branch/repo/name` | version | `branch/repo/arch/name-version.apk`, arch; `apk_files` |
| cargo | `cargo_crates` | 1:1 | name | version | `name-version.crate`; `yanked` in `V.metadata` |
| composer | `composer_packages` | 1:1 | `vendor/package` | `version_normalized` | synthesised zip name; `composer_json` in `V.metadata` |
| conan | `conan_packages` | one F per old row, V per recipe+package revision | name | `version@user_channel#rrev` | roles `recipe`/`package`/`manifest`; only the recipe manifest row is `listed`; `conan_files` |
| conda | `conda_packages` | 1:1 (a build's two extensions are two rows, as today) | name | `version-build_string` | `platform/filename`, arch = platform; repodata `depends` as a JSON array, order kept |
| deb | `deb_packages` | 1:1 | name | `version\|arch\|component` | pool path, arch, part = distribution; `deb_files` |
| gomod | `gomod_modules` | one V; F = zip (primary, listed) + mod (role `mod`) | module path | version | `module/@v/version.zip`; `go_mod_content` in the mod row's `attrs` |
| helm | `helm_charts` | 1:1 | name | version | `name-version.tgz` |
| ivy | `ivy_artifacts` | V per revision, F per artifact | `organisation:module` | revision | `organisation/module/revision/artifact` (the stored double extension is kept as is) |
| maven | `maven_artifacts` | V per version, F per file | `group:artifact`, `attrs{group_id,artifact_id}` | version | repository path; `maven_files` |
| npm | `npm_packages`, `npm_versions`, `npm_dist_tags` | P = old package id, V/F = old version id | `@scope/name` | version | tarball filename; `checksums{integrity,shasum}`; manifest in `V.metadata`; dist-tags → `package_tags`; `packument_stale` in `P.attrs` |
| nix | `nix_paths` | 1:1 | name | `store_hash` | `<hash>.narinfo` (the store hash is repository-wide unique, so it is the path) |
| nuget | `nuget_packages` | 1:1 | package id | version | `id.version.nupkg` |
| pacman | `pacman_packages` | 1:1 | name | `version\|arch` | filename, arch; `pacman_files` |
| pypi | `pypi_packages` | V per release, F per distribution file | normalised name | version | filename; arch = python tag; `package_type` in `attrs` |
| raw | `raw_artifacts` | one P+V+F per path | the path | `''` | the path |
| rpm | `rpm_packages` | 1:1 | name | `version-release:epoch` | `name-version-release.arch.rpm`, arch; `size_bytes = package_size`; listing extras in `attrs`; `rpm_files` |
| rubygems | `rubygems_gems` | 1:1 (platform in the key) | name | `version[-platform]` | `name-version[-platform].gem`, arch = platform; `size_bytes = size` |
| sbt | `sbt_artifacts` | V per version, F per file | `group:artifact` | version | filename, arch = scala version |
| terraform | `terraform_modules`, `terraform_providers` | modules 1:1; providers V per version, F per os/arch | `modules/name/system`, `providers/type` | version | module archive / provider zip; arch = `os_arch` |

Each port documents its exact column-to-JSON mapping in the header of its `internal/database/pkg_<format>.go` and in the migration function that imports the old rows, and an integration round-trip test proves `old row → import → typed read` equals the old row.

## Cross-format subsystems on the generic tables

| Subsystem | Before | After |
|---|---|---|
| `ListRepoComponents`, `…ByName`, `CountRepoComponents`, `GetRepoComponentBlob`, `ResolveComponentNames` | 22 branches each, two-stage per `package-listing.md` | One generic branch (+ the docker tag and proxy-cache pseudo-branches). Stage 1 takes the top `limit+offset` files by `(created_at DESC, id)` from `idx_package_files_listing` (joined to V and P only for the `name`/`q` filters), stage 2 joins `blobs` for the page. The legacy SQL in `tests/testdata/list_repo_components_legacy.sql` is the oracle of `tests/repo_components_equivalence_test.go`. |
| Catalog roll-up, `catalog_detail`, `repo_usage`, `security` name matching | per-table branches | One branch over F/V/P. `version_count` keeps its meaning (listed files); the plugin's `Detail` hook decodes `V.metadata` for the dependency list. |
| `blobRefSpecs`, `blobRefScopeSpecs` (tiering) | 26 entries | `package_files.blob_id` plus the side tables that own a blob column (docker). Tiering `path_glob` matches `package_files.path` (a documented change for formats whose old tiering path was not their file path). |
| Trash (24 specs), `RestoreTrash`, `PurgeTrash`, `ListTrash` | per-kind table specs | Generic file-scope operations on `package_files` (+ version stamping above). The API's `kind` strings stay: a kind is derived from the plugin that owns the repository's format (one value per format, docker keeps its two). Hooks for the formats that need more (`Trash` capability: npm packument touch and dist-tag purge, docker's manifest→tag cascade stays docker's own). |
| Quota triggers | 24 functions × 3 triggers | One function set on `package_files` (and `docker_blobs`), keyed on `role = 'primary'` for the artifact count |
| Cleanup policies | `formatTable` per format | Generic rank over `(package_id, arch, part)` ordered by `(version created_at, version_key)`, deleting files through the generic trash path; `Cleanup.ProtectExpr` is a SQL fragment (npm tagged versions, newest maven snapshot) |
| Search | 23-branch `UNION ALL` view | One query over F/V/P; checksum search uses the `checksums` expression indexes |
| Promote | 17 `rowSpec`s + 6 bespoke promoters | One generic copy of P/V/F (+ side-table copy hook); npm tags, deb distributions, rpm stale flag, conan revision set, gomod's two files are `Promotion` hooks |
| Backup | `exportFormatTables` with 22 cases and 39 restore funcs | One `packages`/`package_versions`/`package_files`/`package_tags` JSONL set plus the plugin's side tables. Archives written by ≤ 0.14 are still restored through the legacy decoders. |
| Recovery journal | 39 table entries and triggers | `packages`, `package_versions`, `package_files`, `package_tags` + the side tables. Sidecars written by ≤ 0.14 name the old tables: see Migration. |
| Federation | 11 capture triggers, 9 per-format codecs | Capture triggers on V and F keyed on `unit_key`; one generic codec whose unit is a version (V + its files) or a file, as declared by the plugin. The unit keys are unchanged (`name@version`, `name/version/filename`, `repo_path`, ...), so `federation_units` stays valid. The wire row changes shape, so `protocol` is bumped to 2 and a peer on protocol 1 is refused with a clear error: upgrade the members together (docker keeps its codec and its row shape). |

## The plugin

`internal/format/plugin` defines the interface and the registry; `internal/formats` lists the plugins (one line each, no `init()`); each format package has a `plugin.go` returning its value. The package depends only on leaf packages (`repotype`, `models`, `chi`, `webhook`, `clientsetup`), so the proxy, group, vuln, sbom, … libraries import it, not the other way round, and per-format code that was in those libraries moves into the format's package or registers through the plugin.

```go
// Format is the one registration a package format makes. Every method is
// required: a format without a capability returns the explicit zero value
// (plugin.NoProxy, plugin.NoGroup, plugin.NoLifecycle, ...), never nil.
type Format interface {
    // Spec is the static policy (repotype.Spec); repotype.Lookup(name) is the same value.
    Spec() repotype.Spec
    // Hosted builds the hosted-repository handler and mounts its routes on the /repos/{name}
    // subrouter (or the isolated prefix the Spec names).
    Hosted(*Deps) (Hosted, error)
    // Storage declares how the format uses the generic tables.
    Storage() Storage
    // Analysis holds the dependency, license, PURL, manifest, Trivy and OSV hooks.
    Analysis() Analysis
    // ClientSetup builds the configuration snippets for a repository.
    ClientSetup() clientsetup.Builder
    // Proxy and Group are the proxy- and group-repository behaviours.
    Proxy() Proxy
    Group() Group
    // Lifecycle: metadata regeneration after writes, trash restore, rebuild.
    Lifecycle() Lifecycle
}

type Hosted interface {
    RegisterRepoRoutes(chi.Router)
    SetPublisher(webhook.Publisher)
}

type Storage struct {
    Kind        string          // trash kind shown by the API ("cargo_crate", ...)
    TrashScope  Scope           // ScopeFile or ScopeVersion: what a trash request removes
    Cleanup     Cleanup         // ProtectExpr and the version ordering
    Federation  *FederationUnit // nil: not federated (Spec.Federation says the same)
    Detail      Detail          // dependency/info decoding of V.metadata for the artifact detail page
    Promotion   Promotion       // side-table copy and post-copy hooks (nil hooks = generic copy only)
    Trash       Trash           // OnTrash/OnRestore/OnPurge (nil = none)
    SideTables  []SideTable     // journaled side tables, parents first, with BlobRefs/Exclude
    Dedicated   *Dedicated      // non-nil only for a format that does not use the generic tables (docker)
}
```

`Proxy` carries the predicates the proxy package switches on today (`Immutable`, `IsMetadata`, `IsBlobPath`, `PackageFile`, `FetchOpts`) and the serve entry point (`Serve(*proxy.Service, …)`, nil-safe through `NoProxy`); `Group` carries `ServeMerged`, `PackageRef` and `AuthPath`. The pipeline types in `internal/proxy/pipeline.go` and `internal/group/pipeline.go` are exported where a plugin needs them. A format that is not proxyable or groupable returns `plugin.NoProxy`/`plugin.NoGroup` and `Spec.Proxy`/`Spec.Group` say the same; a test asserts the two agree.

### What becomes data and what stays code

| Decision point (every place that switches on the format today) | After |
|---|---|
| Router mounts: `newFormatHandlers`, `hostedRouters`, `mountRepoRoutes`, `mountIsolatedRepoRoutes`, the `SetPublisher` list, REST upload routes, `metrics_upload.go` routes | a loop over `formats.All()`; placement from `Spec.Isolated`; guards and extra uploads are optional capabilities of the `Hosted` value |
| proxy `formatServer`, `fetch`, `Immutable`, `isMetadataKey`, `packageDir`, `packageExts`, `isHTMLForPackage`, `presign.IsProxyBlobPath` | `Proxy` methods |
| group `serveMerged`, `packageRef`, `classifyDebRPM`, hosted-wins package probes (conda/ivy/sbt name queries) | `Group` methods; the probes become one `PackageIndex.HasPackage(repo, name)` over `packages` |
| worker cleanup notifiers, `refreshFormatMetadata`, `notifyRepo`, `regenerateRestored*`, `gpgkeys.refresh` | `Lifecycle` |
| `pkgdeps`, `licensemeta`, `vuln` PURL/Trivy, `sbom` PURL, `ai/manifest`, `artifact_detail` decoders | `Analysis` |
| `clientsetup.builderFor` | `ClientSetup()` |
| `rpm sign_packages`, REST upload hints, `downloadFilename`, `withDistro` | `Spec` fields or `Hosted`/`Storage` hooks |

The `format_exhaustive_test.go` files whose only job was to pin one of those switches are deleted. Tests that still guard a real per-format *decision* stay (for example which formats a federation codec exists for, asserted against `Spec.Federation`).

## Migration

Constraints: forward-only; large tables; no outage for a rolling upgrade (the previous release's replicas keep serving while the first new replica migrates); ids preserved; the recovery journal and quota counters must not double-count.

**Expand / backfill / switch in this release, contract in the next.** The old tables stay for one release so that a still-running previous-release replica keeps reading and writing them:

1. `098` creates the generic tables, indexes (instant on empty tables) and the plpgsql helpers: one importer function `oa_import_file(...)` that upserts P, V and F (and returns the file id), idempotent on the preserved ids.
2. `099…` define one `oa_import_<old_table>(row)` function per old table: pure argument mapping onto the shared importer, plus the side-table insert. Each also installs an `AFTER INSERT OR UPDATE` and an `AFTER DELETE` **mirror trigger** on the old table that calls it (setting `oa.mirror = 'on'` for the transaction), so a write by a previous-release replica during the rollout appears in the generic tables. The mirror is one-way (old → new); the new binary never writes the old tables.
3. A re-runnable `.no_tx.sql` backfill copies every old table in primary-key batches (`SELECT oa_import_x(t) FROM x t WHERE id > $last ORDER BY id LIMIT 5000` in a `DO` loop, one commit per batch), so locks are short, a crash resumes, and re-running changes nothing.
4. After the copy, the generic-table triggers are created: `quota_pending_*` and the federation capture skip rows written while `oa.mirror` is on (the old table's own trigger already accounted for them); `recovery_journal` is not skipped, so mirrored rows are journaled, and the reconcile pass journals the backfilled rows. The old tables' own `recovery_journal` and `update_updated_at` triggers are dropped and their blob foreign keys are dropped (a purge in the new schema must be able to free a blob that a stale old row still names).
5. The code switches to the generic tables in the same release. The old tables are not read or written by the new binary.
6. **Contract (next release):** drop the mirror triggers, the import functions and the old tables. It is a separate migration because a previous-release replica in a rolling upgrade still reads the old tables, and because the old tables are the rollback path for one release. Until then the old tables are listed in `NotJournaled` ("legacy, dropped in the next release") and in the recovery registry's legacy list.

Rolling-upgrade behaviour: replicas of the previous release keep serving reads from the old tables during the rollout; their uploads and deletes are mirrored; a delete made by a new replica is not visible to an old replica (documented; the window is the rollout). Downgrading after the switch loses what was published since.

**Recovery journal.** `recovery_dirty`/`recovery_sidecars` rows and the sidecar blobs name the old tables. Sidecars written by ≤ 0.14 still rebuild: the registry keeps a `Legacy` list; a legacy sidecar is inserted into the old table (which is still there) and the mirror trigger converts it, then the normal rebuild order continues. New sidecars name the generic tables. `SidecarMagic` is unchanged because the envelope is unchanged; the table names are the discriminator. After the contract release, rebuilding from storage written by ≤ 0.14 requires going through this release first (documented in the rebuild guide).

**Proof.** The migration is proven on a database seeded by the previous release's binary with every format published by its real client: row counts per old table against `packages`/`package_versions`/`package_files` per format, then an install-only run of each client against the new binary on that database, plus a per-format round-trip test (old row JSON equals the typed read of its imported rows). The numbers are in the final report of the change.

## Adding a format after this change

Checklist (about ten files; docs and tests included):

1. `internal/repotype/repotype.go`: one `Spec` entry.
2. `migrations/NNN_<format>.sql` + `…_validate.no_tx.sql`: widen `chk_repo_format` (the only schema work).
3. `internal/format/<format>/` : `http.go` (routes), `parse.go` (upload → version + files), `plugin.go` (the registration).
4. `internal/formats/formats.go`: one line.
5. `internal/proxy/<format>.go`, `internal/group/<format>.go` only when `Spec.Proxy`/`Spec.Group` are set.
6. `scripts/smoke/<format>.sh`, `docs/formats/<format>.md`.
7. `web/src/lib/format-ui.ts` colour and icon (the generated lists come from the registry by `make generate`).

No table, no union branch, no trigger, no trash/quota/cleanup/backup/promote/recovery entry, no exhaustiveness test.

## Risks and what is deferred

- Docker stays dedicated (see Scope); each cross-format subsystem keeps exactly one docker special case, declared through `Storage.Dedicated`.
- The `package_files.path` change for tiering `path_glob`, the search display names of ivy/sbt (`org:module` like the listing instead of `org/module`) and the federation protocol bump are intentional behaviour changes, listed in the upgrade notes.
- Performance of the single listing branch is checked with `EXPLAIN` against a seeded repository of 1 M files before the old branches are removed; the two-stage shape and the `(repository_id, created_at DESC, id)` index are kept per `package-listing.md`.
- The contract migration (dropping the old tables, mirror triggers and import functions) and removing the legacy recovery/backup decoders are the next release's work.
