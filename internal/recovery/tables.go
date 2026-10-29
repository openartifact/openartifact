// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package recovery implements disaster recovery from the blob store: the row
// journal (database triggers mark changed package rows, a worker mirrors them
// as small JSON sidecar blobs next to the content-addressed package blobs) and
// `openartifact rebuild`, which re-indexes an empty database from those
// sidecars. See docs/operations/rebuild-from-storage.md.
package recovery

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	formatstorage "github.com/openartifact/openartifact/internal/format/storage"
)

// Column types a journal key may use. They double as the SQL cast used for
// typed keyset lookups, so the primary-key index is always usable.
const (
	keyUUID = "uuid"
	keyText = "text"
)

// KeyColumn is one column of a journaled table's primary key.
type KeyColumn struct {
	Name string
	Type string // keyUUID or keyText
}

// Table is one journaled table. The registry is made of compile-time constants
// (the core tables here, the storage descriptors of the formats): table and
// column names that reach SQL always come from it (or are intersected with it),
// never from sidecar or storage content.
type Table struct {
	Name string
	// Key is the primary key; its order defines the row key encoding.
	Key []KeyColumn
	// Exclude lists columns that are never journaled: generated columns,
	// secrets, volatile counters and state that is rebuilt or re-derived
	// (the rebuild leaves them at their column default).
	Exclude []string
	// BlobRefs lists columns holding a blobs.id: a row is only restorable when
	// the bytes of every blob it references exist in storage.
	BlobRefs []string
}

func idKey() []KeyColumn { return []KeyColumn{{"id", keyUUID}} }

// Registry is the ordered journal registry, parents before children (the
// rebuild inserts in this order). It is built from the compiled-in
// repository-level and docker tables, the four generic package tables
// (packages, package_versions, package_files, package_tags) and the side
// tables the registered storage descriptors declare (storage.Storage.SideTables;
// they follow package_files, their parent). Build it after the formats have
// registered their descriptors: a table that carries the journal trigger but is
// missing here is not mirrored (the Worker reports it, TestRegistryCoversSchema
// pins the schema against the registry). A new package table needs an entry
// here, or an explicit entry in NotJournaled with the reason.
type Registry struct {
	tables []Table
	byName map[string]int
}

// descriptors are the storage descriptors a registry is built from, by format.
type descriptors map[string]formatstorage.Storage

// registered returns the storage descriptors registered at this moment.
func registered() descriptors {
	out := make(descriptors)

	for _, format := range formatstorage.Names() {
		out[format], _ = formatstorage.Lookup(format)
	}

	return out
}

// NewRegistry builds the registry from the storage descriptors registered at
// this moment. A side table listed by two formats is journaled once.
func NewRegistry() *Registry { return newRegistry(registered()) }

func newRegistry(descs descriptors) *Registry {
	head, tail := headTables(), tailTables()

	taken := make(map[string]bool, len(head)+len(tail))
	for _, t := range slices.Concat(head, tail) {
		taken[t.Name] = true
	}

	tables := slices.Concat(head, sideTables(taken, descs), tail)

	r := &Registry{tables: tables, byName: make(map[string]int, len(tables))}
	for i := range tables {
		r.byName[tables[i].Name] = i
	}

	return r
}

// headTables are the tables journaled before the side tables: repository level
// rows, then the generic package tables.
func headTables() []Table {
	return []Table{
		{Name: "projects", Key: idKey(), Exclude: []string{"owner_id"}},
		{Name: "blobs", Key: idKey(), Exclude: []string{"storage_backend_id", "last_accessed_at", "tier_changed_at"}},
		{Name: "repositories", Key: idKey(), Exclude: []string{"upstream_password_enc"}},
		{Name: "repository_group_members", Key: []KeyColumn{{"group_repo_id", keyUUID}, {"member_repo_id", keyUUID}}},
		{Name: "rpm_repos", Key: idKey(), Exclude: []string{"metadata_stale", "updated_at"}},
		{Name: "debian_distributions", Key: idKey()},
		{Name: "debian_components", Key: idKey()},
		{Name: "debian_architectures", Key: idKey()},
		{Name: "packages", Key: idKey()},
		{Name: "package_versions", Key: idKey()},
		{Name: "package_files", Key: idKey(), Exclude: []string{"download_count"}, BlobRefs: []string{"blob_id"}},
		{Name: "package_tags", Key: idKey()},
	}
}

// tailTables are the docker tables (docker keeps dedicated tables) and the
// artifact tables, journaled after the package tables.
func tailTables() []Table {
	return []Table{
		{Name: "docker_blobs", Key: idKey(), Exclude: []string{"last_used_at"}, BlobRefs: []string{"blob_id"}},
		{Name: "docker_blob_images", Key: []KeyColumn{{"repository_id", keyUUID}, {"image", keyText}, {"digest", keyText}}},
		{Name: "docker_manifests", Key: idKey(), BlobRefs: []string{"blob_id"}},
		{Name: "docker_manifest_refs", Key: []KeyColumn{{"manifest_id", keyUUID}, {"kind", keyText}, {"digest", keyText}}},
		{Name: "docker_tags", Key: idKey()},
		{Name: "artifact_refs", Key: idKey()},
		{Name: "artifact_properties", Key: idKey()},
	}
}

// identPattern is what a descriptor-supplied table or column name must match
// before it is spliced into SQL.
var identPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// sideTables lists the side tables of the storage descriptors in
// format order (names sorted), each table once and none that taken already
// names. Descriptors are compile-time constants; a name that is not a plain
// identifier is a programming error.
func sideTables(taken map[string]bool, descs descriptors) []Table {
	var out []Table

	for _, format := range slices.Sorted(maps.Keys(descs)) {
		for _, st := range descs[format].SideTables {
			if taken[st.Name] {
				continue
			}

			taken[st.Name] = true

			if !plainIdentifiers(append([]string{st.Name}, st.Exclude...)) {
				panic(fmt.Sprintf("recovery: format %s: side table %q or its excluded columns are not plain identifiers", format, st.Name))
			}

			out = append(out, Table{Name: st.Name, Key: idKey(), Exclude: slices.Clone(st.Exclude)})
		}
	}

	return out
}

func plainIdentifiers(names []string) bool {
	return !slices.ContainsFunc(names, func(n string) bool { return !identPattern.MatchString(n) })
}

// Tables returns the registry entries in journal order. The slice is shared:
// callers must not modify it.
func (r *Registry) Tables() []Table { return r.tables }

// Len is the number of journaled tables.
func (r *Registry) Len() int { return len(r.tables) }

// Names returns the table names in journal order.
func (r *Registry) Names() []string {
	names := make([]string, len(r.tables))
	for i := range r.tables {
		names[i] = r.tables[i].Name
	}

	return names
}

// Lookup returns the registry entry of a table.
func (r *Registry) Lookup(name string) (*Table, bool) {
	i, ok := r.byName[name]
	if !ok {
		return nil, false
	}

	return &r.tables[i], true
}

// NotJournaled lists every table that has a foreign key to blobs or
// repositories but is deliberately not journaled, with the reason. Together
// with the Registry it must cover all of them (TestRegistryCoversSchema).
//
//nolint:gochecknoglobals // compiled-in allowlist, immutable after init
var NotJournaled = map[string]string{
	"apk_signing_keys":  "holds the encrypted private key of the repository index; a secret outside the journal",
	"nix_pending_nars":  "staging of NAR files that await their narinfo; swept by the cleanup worker",
	"nix_signing_keys":  "holds the encrypted private key of the narinfo signatures; a secret outside the journal",
	"audit_logs":        "audit trail; kept by database backups",
	"bundle_promotions": "release bundle history; operational data covered by database backups",
	"cleanup_policies": "operator policy configuration; recreated with `oa apply` or restored from a database " +
		"backup (a restored policy could start deleting packages)",
	"dependency_track_pushes": "integration state, regenerated on the next push",
	"federation_repos": "federation log state; a rebuilt instance starts a new log (new log_id), so peers reset " +
		"their cursor and run a full merge",
	"group_metadata_state":          "regenerated group metadata bookkeeping",
	"group_package_owners":          "derived from the group members; regenerated",
	"gpg_key_repositories":          "signing key assignment; keys themselves are secrets outside the journal",
	"license_policies":              "operator policy configuration; `oa apply` or database backups",
	"package_deltas":                "derived delta packages; regenerated on demand",
	"presigned_uploads":             "transient in-flight uploads",
	"proxy_cache_entries":           "cache of an upstream; refilled on demand",
	"release_bundle_items":          "release bundle content; covered by database backups",
	"repository_attestation_attach": "per-repository integration switch; `oa apply` or database backups",
	"repository_dependency_track":   "integration configuration with secrets; database backups",
	"repository_metadata":           "generated metadata (helm index, npm packument cache, ...); regenerated by the rebuild",
	"sbom_results":                  "scan output; regenerated by a new scan",
	"storage_quotas":                "usage counters are recomputed by the rebuild; limits are operator configuration",
	"vex_statements":                "operator decisions about vulnerabilities; database backups",
	"webhooks":                      "contain signing secrets; `oa apply` or database backups",
	"docker_uploads":                "transient in-flight blob uploads",
}

// KeyExpr returns the SQL expression yielding the canonical row key of the
// table's row aliased alias: the key column as text for a single-column key, the
// jsonb array of the columns (as text) otherwise. It matches what the database
// triggers of migration 068 compute.
func (t *Table) KeyExpr(alias string) string {
	if len(t.Key) == 1 {
		return fmt.Sprintf("%s.%s::text", alias, t.Key[0].Name)
	}

	parts := make([]string, len(t.Key))
	for i, k := range t.Key {
		if k.Type == keyUUID {
			parts[i] = fmt.Sprintf("%s.%s::text", alias, k.Name)
		} else {
			parts[i] = fmt.Sprintf("%s.%s", alias, k.Name)
		}
	}

	return "jsonb_build_array(" + strings.Join(parts, ", ") + ")::text"
}

// KeyCols returns the quoted, alias-qualified key columns, comma separated.
func (t *Table) KeyCols(alias string) string {
	cols := make([]string, len(t.Key))
	for i, k := range t.Key {
		cols[i] = alias + "." + k.Name
	}

	return strings.Join(cols, ", ")
}

// ExcludeSQL returns the SQL text[] literal of the excluded columns.
func (t *Table) ExcludeSQL() string {
	if len(t.Exclude) == 0 {
		return "ARRAY[]::text[]"
	}

	q := make([]string, len(t.Exclude))
	for i, c := range t.Exclude {
		q[i] = "'" + c + "'"
	}

	return "ARRAY[" + strings.Join(q, ",") + "]::text[]"
}

// isExcluded reports whether the column is excluded from the journal.
func (t *Table) isExcluded(col string) bool {
	return slices.Contains(t.Exclude, col)
}

// SplitKey decodes a canonical row key into one text value per key column. It
// reports false when the key does not have the table's shape.
func (t *Table) SplitKey(key string) ([]string, bool) {
	parts := []string{key}

	if len(t.Key) > 1 {
		var ok bool

		if parts, ok = decodeKeyArray(key); !ok || len(parts) != len(t.Key) {
			return nil, false
		}
	}

	for i, k := range t.Key {
		if !validKeyPart(k.Type, parts[i]) {
			return nil, false
		}
	}

	return parts, true
}

// decodeKeyArray parses the jsonb array form of a composite row key.
func decodeKeyArray(key string) ([]string, bool) {
	var parts []string
	if err := json.Unmarshal([]byte(key), &parts); err != nil {
		return nil, false
	}

	return parts, true
}

// validKeyPart reports whether v is a well-formed value for the key column kind:
// a canonical lowercase UUID, or any non-empty text.
func validKeyPart(kind, v string) bool {
	if v == "" {
		return false
	}

	if kind == keyUUID {
		return uuidPattern.MatchString(v)
	}

	return true
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
