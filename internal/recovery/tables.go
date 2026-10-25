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
	"regexp"
	"slices"
	"strings"
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

// Table is one journaled table. The registry is compiled in: table and column
// names that reach SQL always come from here (or are intersected with it), never
// from sidecar or storage content.
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

// Tables is the ordered journal registry, parents before children (the rebuild
// inserts in this order). A new package table needs an entry here, or an
// explicit entry in NotJournaled with the reason: TestRegistryCoversSchema
// (integration) fails otherwise. Keep migration 068's trigger list in step.
//
//nolint:gochecknoglobals // compiled-in registry, immutable after init
var Tables = []Table{
	{Name: "projects", Key: idKey(), Exclude: []string{"owner_id"}},
	{Name: "blobs", Key: idKey(), Exclude: []string{"storage_backend_id", "last_accessed_at", "tier_changed_at"}},
	{Name: "repositories", Key: idKey(), Exclude: []string{"upstream_password_enc"}},
	{Name: "repository_group_members", Key: []KeyColumn{{"group_repo_id", keyUUID}, {"member_repo_id", keyUUID}}},
	{Name: "rpm_repos", Key: idKey(), Exclude: []string{"metadata_stale", "signing_key_id", "signing_key_fingerprint", "updated_at"}},
	{Name: "debian_distributions", Key: idKey()},
	{Name: "debian_components", Key: idKey()},
	{Name: "debian_architectures", Key: idKey()},
	{Name: "deb_packages", Key: idKey(), Exclude: []string{"search_tsv"}, BlobRefs: []string{"blob_id"}},
	{Name: "rpm_packages", Key: idKey(), Exclude: []string{"search_tsv"}, BlobRefs: []string{"blob_id"}},
	{Name: "maven_artifacts", Key: idKey(), Exclude: []string{"search_tsv"}, BlobRefs: []string{"blob_id"}},
	{Name: "ivy_artifacts", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "sbt_artifacts", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "npm_packages", Key: idKey(), Exclude: []string{"packument_stale"}},
	{Name: "npm_versions", Key: idKey(), Exclude: []string{"search_tsv"}, BlobRefs: []string{"blob_id"}},
	{Name: "npm_dist_tags", Key: idKey()},
	{Name: "pypi_packages", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "nuget_packages", Key: idKey(), Exclude: []string{"download_count"}, BlobRefs: []string{"blob_id"}},
	{Name: "gomod_modules", Key: idKey(), BlobRefs: []string{"zip_blob_id", "mod_blob_id"}},
	{Name: "cargo_crates", Key: idKey(), Exclude: []string{"download_count"}, BlobRefs: []string{"blob_id"}},
	{Name: "rubygems_gems", Key: idKey(), Exclude: []string{"download_count"}, BlobRefs: []string{"blob_id"}},
	{Name: "conda_packages", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "conan_packages", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "helm_charts", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "raw_artifacts", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "docker_blobs", Key: idKey(), Exclude: []string{"last_used_at"}, BlobRefs: []string{"blob_id"}},
	{Name: "docker_blob_images", Key: []KeyColumn{{"repository_id", keyUUID}, {"image", keyText}, {"digest", keyText}}},
	{Name: "docker_manifests", Key: idKey(), BlobRefs: []string{"blob_id"}},
	{Name: "docker_manifest_refs", Key: []KeyColumn{{"manifest_id", keyUUID}, {"kind", keyText}, {"digest", keyText}}},
	{Name: "docker_tags", Key: idKey()},
	{Name: "artifact_refs", Key: idKey()},
	{Name: "artifact_properties", Key: idKey()},
}

// NotJournaled lists every table that has a foreign key to blobs or
// repositories but is deliberately not journaled, with the reason. Together
// with Tables it must cover all of them (TestRegistryCoversSchema).
//
//nolint:gochecknoglobals // compiled-in allowlist, immutable after init
var NotJournaled = map[string]string{
	"audit_logs":                    "audit trail; kept by database backups",
	"bundle_promotions":             "release bundle history; operational data covered by database backups",
	"cleanup_policies":              "operator policy configuration; recreated with `oa apply` or restored from a database backup (a restored policy could start deleting packages)",
	"dependency_track_pushes":       "integration state, regenerated on the next push",
	"federation_repos":              "federation log state; a rebuilt instance starts a new log (new log_id), so peers reset their cursor and run a full merge",
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
	"repository_metadata":           "generated metadata (npm packument cache, helm index, ...); regenerated by the rebuild",
	"sbom_results":                  "scan output; regenerated by a new scan",
	"storage_quotas":                "usage counters are recomputed by the rebuild; limits are operator configuration",
	"vex_statements":                "operator decisions about vulnerabilities; database backups",
	"webhooks":                      "contain signing secrets; `oa apply` or database backups",
	"docker_uploads":                "transient in-flight blob uploads",
}

// byName indexes Tables.
//
//nolint:gochecknoglobals // derived from the immutable registry
var byName = func() map[string]*Table {
	m := make(map[string]*Table, len(Tables))
	for i := range Tables {
		m[Tables[i].Name] = &Tables[i]
	}

	return m
}()

// Lookup returns the registry entry of a table.
func Lookup(name string) (*Table, bool) {
	t, ok := byName[name]

	return t, ok
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
