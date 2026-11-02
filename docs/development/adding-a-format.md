<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Adding a package format

Read this page if you add support for a new package format, for example a new language registry. It lists every place a format touches, in order, and walks through a worked example.

A format is one registration plus its handler and its store. Nothing else enumerates formats. The generic engines (listing, search, trash, quota, cleanup, tiering, promote, backup, recovery, federation) read the generic tables `packages`, `package_versions` and `package_files`, and the format's [`storage.Storage` descriptor](../design/format-plugins.md#the-storage-descriptor). There is no table list, union branch, trigger or exhaustiveness test to extend. Docker is the only format with dedicated tables and is not a template.

## The checklist

Run `go test ./...` after each item. The tests that remain name every place that still lacks the format: `TestRegistrationsMatchSpecs`, `TestSQLFormatLists`, `TestOpenAPIFormatEnums`, `TestSharedRouteClashesAreKnown`, the upload-label tests and the web tests.

1. **Registry.** One `Spec` in `internal/repotype/repotype.go`, in alphabetical order. It holds proxy, group and federation support, the default upstream, signing, `Mount`, version rules, PURL type and OSV ecosystem. Add the upload routes in `internal/repotype/uploads.go`. These files are the only place format facts live; never add a slice or `switch` of format names.
2. **Database constraint.** Two migrations in `migrations/`: `NNN_<format>.sql` drops and re-adds `chk_repo_format` as `NOT VALID`, and `NNN_<format>_validate.no_tx.sql` runs `VALIDATE CONSTRAINT`. `NNN` is the next free number: run `ls migrations` and add one to the highest. Also add the format to the enums of `internal/api/openapi.json`. No table: the format lives in the generic tables.
3. **Handler.** `internal/format/<format>/` holds the protocol routes, publish, download and tests. Write it against a small store interface.
4. **Typed store.** The files are:
   - `internal/database/pkg_<format>.go`: reads and writes through `writePackage` and `deleteFiles` in `pkg_generic.go`. Its doc comment states the mapping to `packages`, `package_versions`, `package_files` and the `unit_key`.
   - `internal/database/queries/<format>.sql`, then `make generate`.
   - `internal/database/models/<format>.go`.
   - The embed in `internal/database/stores.go`, plus the interface in `internal/format/formattest` if its fake store is used.

   Add a side table only when a foreign key, an index or bulk data demands one.
5. **Registration.** `internal/formats/<format>.go` is a `plugin.Format`, usually a `plugin.Definition`. It holds the hosted constructor, analysis hooks, the client-setup builder, proxy and group hooks (or `NoProxy` and `NoGroup`), lifecycle hooks, and the `storage.Storage` descriptor with its trash kind, cleanup partition, federation, detail, promotion and backup values. Add one line to `registrations()` in `internal/formats/formats.go`; the order there is the shared-route precedence.
6. **Client setup.** Add the builder in `internal/clientsetup/clientsetup.go` (snippets and base URL template). `make generate` rewrites `web/src/lib/formats.generated.ts` and `clientsetup.generated.ts` from the registry and this package with `cmd/genformats`. CI fails when a committed file is stale.
7. **Web.** Add `FORMAT_META` in `web/src/lib/formats.ts` (label, colour, icon; `bun run typecheck` fails until it exists) and the install command in `web/src/lib/installCommands.ts`. Touch `PackageUploadZone.vue` when the format has no browser upload.
8. **Metrics label.** Add `Format<Name>` in `internal/metrics/uploads.go`. `TestEveryFormatHasUploadLabel` maps every format to one.
9. **Client smoke test.** Add `scripts/smoke/<format>.sh`: publish a tiny package with the real client and install it back. See [client compatibility matrix](ci-and-releases.md#client-compatibility-matrix). The nightly matrix picks the script up by itself.
10. **Docs.** Add `docs/formats/<format>.md` and a row in `docs/formats/README.md`. `make docs-gen` refreshes `docs/reference`.

Optional, only when the format needs it:

- `models.TrashKind<Name>`, when the API names the kind.
- `internal/presign/paths.go`, for the presigned blob paths of a proxy.
- The `oa import` source: `cmd/oa/import_fmt_<format>.go` and `import_source.go`.
- `cmd/oa/cmd_ci_upload.go`, for `oa ci upload`.
- `restUploadHandlers()` in `cmd/server/app_routes.go`, for a REST upload endpoint in `internal/api`.

## Worked example: a hypothetical `swift`

The example is the Swift Package Registry, hosted only. Assume one package per `scope.name`, one version per release, one source archive file per version, and no proxy, group or federation. `NNN` stands for the next free migration number.

| # | File | What goes in it |
|---|---|---|
| 1 | `internal/repotype/repotype.go` | `{Name: "swift", PURLType: "swift", OSVEcosystem: "SwiftURL", Mount: MountIsolated}` |
| 1 | `internal/repotype/uploads.go` | The `PUT /repos/{repo}/{scope}/{name}/{version}` route as an upload endpoint |
| 2 | `migrations/NNN_swift.sql` | Drop and re-add `chk_repo_format` with `'swift'` in the list, `NOT VALID` |
| 2 | `migrations/NNN_swift_validate.no_tx.sql` | `ALTER TABLE repositories VALIDATE CONSTRAINT chk_repo_format;` |
| 2 | `internal/api/openapi.json` | `"swift"` in each format enum |
| 3 | `internal/format/swift/http.go`, `publish.go`, `*_test.go` | The registry protocol: `GET /{scope}/{name}`, `.../{version}`, `.../{version}.zip`, `Package.swift` |
| 4 | `internal/database/pkg_swift.go` | `PutSwiftRelease`, `GetSwiftRelease`, `ListSwiftReleases` (see below) |
| 4 | `internal/database/queries/swift.sql`, `models/swift.go`, `stores.go` | The format-specific reads, the model, `*SwiftStore` embedded in `PostgresDB` |
| 5 | `internal/formats/swift.go` | `plugin.Definition{ID: "swift", NewHosted: ..., Snippets: ..., Store: storage.Storage{Trash: ..., Detail: ...}}` |
| 5 | `internal/formats/formats.go` | `swiftFormat(),` in `registrations()` |
| 6 | `internal/clientsetup/clientsetup.go` | The `swift package-registry set` snippet |
| 7 | `web/src/lib/formats.ts`, `installCommands.ts` | Label, colour, icon and the `swift package add` command |
| 8 | `internal/metrics/uploads.go` | `FormatSwift = "swift"` |
| 9 | `scripts/smoke/swift.sh` | Publish with `swift package-registry publish`, resolve it back |
| 10 | `docs/formats/swift.md`, `docs/formats/README.md` | The user page and the row |

The store functions in item 4 write these rows:

- One `packages` row: `name = scope.name`, `name_key` is the lower-cased name.
- One `package_versions` row: `version_key = version`.
- One `package_files` row: `path = scope/name/version.zip`, `role = primary`, `listed`.
- A duplicate release maps to `ErrDuplicate`.

That is 10 checklist items over 21 files. `make generate` also rewrites `dbsqlc/`, `formats.generated.ts` and `clientsetup.generated.ts`; those are not counted.

Nothing in `internal/search`, `internal/trash`, `internal/backup`, `internal/promote`, `internal/federation`, `internal/recovery`, `internal/worker` or the catalog and listing queries changes. Adding swift to them is the descriptor in item 5.

For proxy and group support, write the format's grammar in `pkg/<format>`, then its proxy hooks (`internal/proxy/<format>.go`) and merge hook (`internal/group/<format>.go`). Do not copy the resolve, revalidate and read sequence; see [proxy and group pipeline](architecture.md#proxy-and-group-pipeline).

## The row journal

A format that stores its files in the generic package tables needs no journal entry of its own: its descriptor's `SideTables` are added to the registry automatically (`NewRegistry().Tables()`), and `packages`, `package_versions`, `package_files` and `package_tags` are already journaled. Any other table with a foreign key to `blobs` or `repositories` needs an entry in `internal/recovery/tables.go` (`Tables`, parents first) and a `recovery_journal` trigger in a new forward-only migration, or an entry with a reason in `NotJournaled`. `TestRegistryCoversSchema` fails otherwise. See `.agents/rules/recovery-journal.md` and [Rebuild from storage](../operations/rebuild-from-storage.md).
