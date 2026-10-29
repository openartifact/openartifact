// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// OSVStore owns the mirrored OSV advisories and per-package OSV scan results.
type OSVStore struct{ *core }

var _ OSVMirror = (*PostgresDB)(nil)

// OSVMirror is the local mirror of the OSV database and the lookups its re-match
// needs. It is implemented by *PostgresDB and found with a type assertion on
// Database (like AuditRetentionStore).
type OSVMirror interface {
	// ApplyOSVAdvisories stores the advisories that are new or modified later
	// than the stored copy (the others are skipped) and returns the packages
	// whose advisories changed: the old and the new affected packages of every
	// changed record. It flags the ecosystems of those packages as having a
	// re-match pending, in the same transaction.
	ApplyOSVAdvisories(ctx context.Context, advisories []models.OSVAdvisory) ([]models.OSVPackageKey, error)

	// ListOSVAdvisoryIDs returns the ids of the advisories that have an
	// affected package in the ecosystem (withdrawn ones included).
	ListOSVAdvisoryIDs(ctx context.Context, ecosystem string) ([]string, error)

	// RemoveOSVAdvisories drops the affected packages of the ecosystem from
	// the given advisories, which its export no longer lists, and deletes the
	// advisories (and their aliases) left without any affected package. It
	// returns the packages that lost an advisory and flags the ecosystem as
	// having a re-match pending, in the same transaction.
	RemoveOSVAdvisories(ctx context.Context, ecosystem string, ids []string) ([]models.OSVPackageKey, error)

	// GetOSVMirrorState returns the import state of an ecosystem, nil when it
	// has none.
	GetOSVMirrorState(ctx context.Context, ecosystem string) (*models.OSVMirrorState, error)

	// ListOSVMirrorStates returns the import state of every ecosystem, by name.
	ListOSVMirrorStates(ctx context.Context) ([]models.OSVMirrorState, error)

	// SaveOSVMirrorState upserts the import state of an ecosystem. It never
	// touches RematchPending, which ApplyOSVAdvisories and
	// FinishOSVRematch own.
	SaveOSVMirrorState(ctx context.Context, st *models.OSVMirrorState) error

	// FinishOSVRematch clears the pending flag of an ecosystem and records when
	// its re-match started and how long it took.
	FinishOSVRematch(ctx context.Context, ecosystem string, started time.Time, took time.Duration) error

	// CountOSVAdvisories counts the live (not withdrawn) advisories affecting
	// an ecosystem.
	CountOSVAdvisories(ctx context.Context, ecosystem string) (int, error)

	// ListOSVAdvisoriesForPackages returns the live advisories that name each
	// of the normalized package names, keyed by that name.
	ListOSVAdvisoriesForPackages(ctx context.Context, ecosystem string, namesNorm []string) (map[string][]models.OSVAdvisoryMatch, error)

	// OSVCVEAliases returns, for each of the given advisory ids (or aliases)
	// the mirror holds, the CVE ids among the aliases of its record; ids the
	// mirror does not hold are absent, so an empty mirror yields an empty map.
	OSVCVEAliases(ctx context.Context, ids []string) (map[string][]string, error)

	// ListStoredPackages pages the package versions hosted repositories hold
	// for a format (one row per repository, name and version), in (repository,
	// name, version) order after the given position (nil: from the start).
	ListStoredPackages(ctx context.Context, format string, after *models.StoredPackage, limit int) ([]models.StoredPackage, error)

	// ListProxyCacheKeys pages the cached files of the proxy repositories of a
	// format, in id order after afterID ("": from the start).
	ListProxyCacheKeys(ctx context.Context, format, afterID string, limit int) ([]models.ProxyCacheKey, error)

	// GetOSVScanResults returns the results of the osv scanner for the given
	// package versions, keyed by OSVResultKey.
	GetOSVScanResults(ctx context.Context, pkgs []models.StoredPackage) (map[string]*models.VulnScanResult, error)

	// SaveOSVScanResult stores a result of the osv scanner for its artifact:
	// it creates the result or replaces the findings of the existing one and
	// leaves its quarantine flag alone (policies own it).
	SaveOSVScanResult(ctx context.Context, result *models.VulnScanResult) error
}

// OSVResultKey is the GetOSVScanResults key of a stored package.
func OSVResultKey(repositoryID, artifactID string) string {
	return repositoryID + "\x00" + artifactID
}

// osvAdvisoryJSON is the wire form of an advisory sent to the upsert.
type osvAdvisoryJSON struct {
	ID         string          `json:"id"`
	Modified   time.Time       `json:"modified"`
	Published  *time.Time      `json:"published"`
	Withdrawn  *time.Time      `json:"withdrawn"`
	Summary    string          `json:"summary"`
	Severity   json.RawMessage `json:"severity"`
	DBSeverity string          `json:"db_severity"`
	CWEIDs     []string        `json:"cwe_ids"`
	Refs       []string        `json:"refs"`
}

type osvAffectedJSON struct {
	AdvisoryID string          `json:"advisory_id"`
	Ecosystem  string          `json:"ecosystem"`
	Name       string          `json:"name"`
	NameNorm   string          `json:"name_norm"`
	Ranges     json.RawMessage `json:"ranges"`
	Versions   []string        `json:"versions"`
}

type osvAliasJSON struct {
	AdvisoryID string `json:"advisory_id"`
	Alias      string `json:"alias"`
}

const (
	osvUpsertAdvisoriesSQL = `
		INSERT INTO osv_advisories (id, modified, published, withdrawn, summary, severity, db_severity, cwe_ids, refs, imported_at)
		SELECT x.id, x.modified, x.published, x.withdrawn, x.summary, x.severity, x.db_severity, x.cwe_ids, x.refs, now()
		FROM jsonb_to_recordset($1::jsonb) AS x(id text, modified timestamptz, published timestamptz, withdrawn timestamptz,
			summary text, severity jsonb, db_severity text, cwe_ids text[], refs text[])
		ON CONFLICT (id) DO UPDATE SET
			modified = EXCLUDED.modified, published = EXCLUDED.published, withdrawn = EXCLUDED.withdrawn,
			summary = EXCLUDED.summary, severity = EXCLUDED.severity, db_severity = EXCLUDED.db_severity,
			cwe_ids = EXCLUDED.cwe_ids, refs = EXCLUDED.refs, imported_at = EXCLUDED.imported_at`

	osvInsertAffectedSQL = `
		INSERT INTO osv_affected (advisory_id, ecosystem, name, name_norm, ranges, versions)
		SELECT x.advisory_id, x.ecosystem, x.name, x.name_norm, x.ranges, x.versions
		FROM jsonb_to_recordset($1::jsonb) AS x(advisory_id text, ecosystem text, name text, name_norm text, ranges jsonb,
			versions text[])`

	osvInsertAliasesSQL = `
		INSERT INTO osv_advisory_aliases (advisory_id, alias)
		SELECT DISTINCT x.advisory_id, x.alias
		FROM jsonb_to_recordset($1::jsonb) AS x(advisory_id text, alias text)
		ON CONFLICT DO NOTHING`

	osvMarkPendingSQL = `
		INSERT INTO osv_mirror_state (ecosystem, rematch_pending)
		SELECT DISTINCT unnest($1::text[]), true
		ON CONFLICT (ecosystem) DO UPDATE SET rematch_pending = true`
)

// ApplyOSVAdvisories implements OSVMirror.
func (p *OSVStore) ApplyOSVAdvisories(ctx context.Context, advisories []models.OSVAdvisory) ([]models.OSVPackageKey, error) {
	if len(advisories) == 0 {
		return nil, nil
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin osv import transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	changed, err := osvChangedAdvisories(ctx, tx, advisories)
	if err != nil {
		return nil, err
	}

	if len(changed) == 0 {
		return nil, nil
	}

	ids := make([]string, len(changed))
	for i := range changed {
		ids[i] = changed[i].ID
	}

	touched, err := osvReplaceAdvisories(ctx, tx, changed, ids)
	if err != nil {
		return nil, err
	}

	keys := osvTouchedKeys(touched, changed)

	ecosystems := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))

	for _, k := range keys {
		if !seen[k.Ecosystem] {
			seen[k.Ecosystem] = true
			ecosystems = append(ecosystems, k.Ecosystem)
		}
	}

	if len(ecosystems) > 0 {
		if _, err := tx.ExecContext(ctx, osvMarkPendingSQL, ecosystems); err != nil {
			return nil, fmt.Errorf("flag osv re-match pending: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit osv import: %w", err)
	}

	return keys, nil
}

// osvChangedAdvisories keeps the advisories that are not stored yet, are newer
// than the stored copy, or affect an ecosystem the stored copy has no package
// of. The last case is a record imported again after its ecosystem was added to
// the mirror: an advisory of several ecosystems is stored with the packages of
// the ecosystems configured at that time, and its modified time did not move.
func osvChangedAdvisories(ctx context.Context, tx *sql.Tx, advisories []models.OSVAdvisory) ([]models.OSVAdvisory, error) {
	ids := make([]string, len(advisories))
	for i := range advisories {
		ids[i] = advisories[i].ID
	}

	stored, err := osvStoredModified(ctx, tx, ids)
	if err != nil {
		return nil, err
	}

	ecosystems, err := osvStoredEcosystems(ctx, tx, ids)
	if err != nil {
		return nil, err
	}

	changed := make([]models.OSVAdvisory, 0, len(advisories))

	for i := range advisories {
		adv := &advisories[i]

		// A copy older than the stored one never replaces it, whatever it adds.
		m, ok := stored[adv.ID]
		if !ok || adv.Modified.After(m) || (!adv.Modified.Before(m) && osvAddsEcosystem(adv, ecosystems[adv.ID])) {
			changed = append(changed, *adv)
		}
	}

	return changed, nil
}

// osvAddsEcosystem reports whether adv affects an ecosystem that has no stored
// package among have.
func osvAddsEcosystem(adv *models.OSVAdvisory, have map[string]bool) bool {
	for i := range adv.Affected {
		if !have[adv.Affected[i].Ecosystem] {
			return true
		}
	}

	return false
}

// osvStoredModified returns the modified time of the stored advisories among ids.
func osvStoredModified(ctx context.Context, tx *sql.Tx, ids []string) (map[string]time.Time, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, modified FROM osv_advisories WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read stored osv advisories: %w", err)
	}
	defer rows.Close()

	stored := make(map[string]time.Time, len(ids))

	for rows.Next() {
		var (
			id       string
			modified time.Time
		)

		if err := rows.Scan(&id, &modified); err != nil {
			return nil, fmt.Errorf("scan stored osv advisory: %w", err)
		}

		stored[id] = modified
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stored osv advisories: %w", err)
	}

	return stored, nil
}

// osvStoredEcosystems returns, per advisory among ids, the ecosystems it has
// stored affected packages in.
func osvStoredEcosystems(ctx context.Context, tx *sql.Tx, ids []string) (map[string]map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT advisory_id, ecosystem FROM osv_affected WHERE advisory_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read stored osv ecosystems: %w", err)
	}
	defer rows.Close()

	have := make(map[string]map[string]bool, len(ids))

	for rows.Next() {
		var id, ecosystem string

		if err := rows.Scan(&id, &ecosystem); err != nil {
			return nil, fmt.Errorf("scan stored osv ecosystem: %w", err)
		}

		if have[id] == nil {
			have[id] = map[string]bool{}
		}

		have[id][ecosystem] = true
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stored osv ecosystems: %w", err)
	}

	return have, nil
}

// osvReplaceAdvisories rewrites the changed advisories with their affected
// packages and aliases and returns the packages they affected before.
func osvReplaceAdvisories(ctx context.Context, tx *sql.Tx, changed []models.OSVAdvisory, ids []string) ([]models.OSVPackageKey, error) {
	old, err := osvAffectedKeys(ctx, tx, ids)
	if err != nil {
		return nil, err
	}

	for _, stmt := range []string{
		`DELETE FROM osv_affected WHERE advisory_id = ANY($1)`,
		`DELETE FROM osv_advisory_aliases WHERE advisory_id = ANY($1)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, ids); err != nil {
			return nil, fmt.Errorf("clear osv advisories: %w", err)
		}
	}

	advs := make([]osvAdvisoryJSON, len(changed))

	var (
		affected []osvAffectedJSON
		aliases  []osvAliasJSON
	)

	for i := range changed {
		a := &changed[i]
		advs[i] = osvAdvisoryJSON{
			ID: a.ID, Modified: a.Modified, Published: a.Published, Withdrawn: a.Withdrawn, Summary: a.Summary,
			Severity: nonNilJSONArray(a.Severity), DBSeverity: a.DBSeverity, CWEIDs: nonNilStrings(a.CWEIDs), Refs: nonNilStrings(a.Refs),
		}

		aliases = append(aliases, osvAliasJSON{AdvisoryID: a.ID, Alias: a.ID})
		for _, alias := range a.Aliases {
			aliases = append(aliases, osvAliasJSON{AdvisoryID: a.ID, Alias: alias})
		}

		for _, af := range a.Affected {
			affected = append(affected, osvAffectedJSON{
				AdvisoryID: a.ID, Ecosystem: af.Ecosystem, Name: af.Name, NameNorm: af.NameNorm,
				Ranges: nonNilJSONArray(af.Ranges), Versions: nonNilStrings(af.Versions),
			})
		}
	}

	for _, step := range []struct {
		sql  string
		rows any
		what string
	}{
		{osvUpsertAdvisoriesSQL, advs, "advisories"},
		{osvInsertAliasesSQL, aliases, "aliases"},
		{osvInsertAffectedSQL, affected, "affected packages"},
	} {
		raw, err := json.Marshal(step.rows)
		if err != nil {
			return nil, fmt.Errorf("encode osv %s: %w", step.what, err)
		}

		if _, err := tx.ExecContext(ctx, step.sql, string(raw)); err != nil {
			return nil, fmt.Errorf("store osv %s: %w", step.what, err)
		}
	}

	return old, nil
}

func nonNilJSONArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}

	return raw
}

func osvAffectedKeys(ctx context.Context, tx *sql.Tx, ids []string) ([]models.OSVPackageKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT ecosystem, name_norm FROM osv_affected WHERE advisory_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read osv affected packages: %w", err)
	}
	defer rows.Close()

	var keys []models.OSVPackageKey

	for rows.Next() {
		var k models.OSVPackageKey
		if err := rows.Scan(&k.Ecosystem, &k.NameNorm); err != nil {
			return nil, fmt.Errorf("scan osv affected package: %w", err)
		}

		keys = append(keys, k)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read osv affected packages: %w", err)
	}

	return keys, nil
}

// osvTouchedKeys unions the old keys with the packages the changed advisories
// affect now, without duplicates.
func osvTouchedKeys(old []models.OSVPackageKey, changed []models.OSVAdvisory) []models.OSVPackageKey {
	seen := make(map[models.OSVPackageKey]bool, len(old))
	keys := make([]models.OSVPackageKey, 0, len(old))

	add := func(k models.OSVPackageKey) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}

	for _, k := range old {
		add(k)
	}

	for i := range changed {
		for _, af := range changed[i].Affected {
			add(models.OSVPackageKey{Ecosystem: af.Ecosystem, NameNorm: af.NameNorm})
		}
	}

	return keys
}

const osvStateColumns = `ecosystem, high_water, etag, archive_modified, source_url, advisories, last_checked_at, last_imported_at,
	last_import_seconds, last_rematch_seconds, last_error, rematch_pending, rematched_at`

func scanOSVState(row rowScanner) (*models.OSVMirrorState, error) {
	var (
		st                                          models.OSVMirrorState
		highWater, checkedAt, importedAt, rematched sql.NullTime
	)

	if err := row.Scan(&st.Ecosystem, &highWater, &st.ETag, &st.ArchiveModified, &st.SourceURL, &st.Advisories, &checkedAt, &importedAt,
		&st.LastImportSeconds, &st.LastRematchSeconds, &st.LastError, &st.RematchPending, &rematched); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap and test sql.ErrNoRows
	}

	if highWater.Valid {
		st.HighWater = &highWater.Time
	}

	if checkedAt.Valid {
		st.LastCheckedAt = &checkedAt.Time
	}

	if importedAt.Valid {
		st.LastImportedAt = &importedAt.Time
	}

	if rematched.Valid {
		st.RematchedAt = &rematched.Time
	}

	return &st, nil
}

// GetOSVMirrorState implements OSVMirror.
func (p *OSVStore) GetOSVMirrorState(ctx context.Context, ecosystem string) (*models.OSVMirrorState, error) {
	st, err := scanOSVState(p.db.QueryRowContext(ctx, `SELECT `+osvStateColumns+` FROM osv_mirror_state WHERE ecosystem = $1`, ecosystem))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // not-found: no state yet
	}

	if err != nil {
		return nil, fmt.Errorf("get osv mirror state: %w", err)
	}

	return st, nil
}

// ListOSVMirrorStates implements OSVMirror.
func (p *OSVStore) ListOSVMirrorStates(ctx context.Context) ([]models.OSVMirrorState, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+osvStateColumns+` FROM osv_mirror_state ORDER BY ecosystem`)
	if err != nil {
		return nil, fmt.Errorf("list osv mirror states: %w", err)
	}
	defer rows.Close()

	var states []models.OSVMirrorState

	for rows.Next() {
		st, err := scanOSVState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan osv mirror state: %w", err)
		}

		states = append(states, *st)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list osv mirror states: %w", err)
	}

	return states, nil
}

// SaveOSVMirrorState implements OSVMirror.
func (p *OSVStore) SaveOSVMirrorState(ctx context.Context, st *models.OSVMirrorState) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO osv_mirror_state (ecosystem, high_water, etag, archive_modified, source_url, advisories, last_checked_at,
			last_imported_at, last_import_seconds, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (ecosystem) DO UPDATE SET
			high_water = EXCLUDED.high_water, etag = EXCLUDED.etag, archive_modified = EXCLUDED.archive_modified,
			source_url = EXCLUDED.source_url, advisories = EXCLUDED.advisories, last_checked_at = EXCLUDED.last_checked_at,
			last_imported_at = EXCLUDED.last_imported_at, last_import_seconds = EXCLUDED.last_import_seconds,
			last_error = EXCLUDED.last_error`,
		st.Ecosystem, st.HighWater, st.ETag, st.ArchiveModified, st.SourceURL, st.Advisories, st.LastCheckedAt, st.LastImportedAt,
		st.LastImportSeconds, st.LastError)
	if err != nil {
		return fmt.Errorf("save osv mirror state: %w", err)
	}

	return nil
}

// FinishOSVRematch implements OSVMirror.
func (p *OSVStore) FinishOSVRematch(ctx context.Context, ecosystem string, started time.Time, took time.Duration) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE osv_mirror_state SET rematch_pending = false, last_rematch_seconds = $2, rematched_at = $3 WHERE ecosystem = $1`,
		ecosystem, took.Seconds(), started)
	if err != nil {
		return fmt.Errorf("finish osv re-match: %w", err)
	}

	return nil
}

// CountOSVAdvisories implements OSVMirror.
func (p *OSVStore) CountOSVAdvisories(ctx context.Context, ecosystem string) (int, error) {
	var n int

	err := p.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT af.advisory_id)
		FROM osv_affected af JOIN osv_advisories a ON a.id = af.advisory_id
		WHERE af.ecosystem = $1 AND a.withdrawn IS NULL`, ecosystem).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count osv advisories: %w", err)
	}

	return n, nil
}

// OSVCVEAliases implements OSVMirror.
func (p *OSVStore) OSVCVEAliases(ctx context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT a.alias, c.alias
		FROM osv_advisory_aliases a
		JOIN osv_advisory_aliases c ON c.advisory_id = a.advisory_id AND c.alias LIKE 'CVE-%'
		WHERE a.alias = ANY($1)
		ORDER BY a.alias, c.alias`, ids)
	if err != nil {
		return nil, fmt.Errorf("osv cve aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, cve string
		if err := rows.Scan(&id, &cve); err != nil {
			return nil, fmt.Errorf("scan osv cve alias: %w", err)
		}

		out[id] = append(out[id], cve)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("osv cve aliases: %w", err)
	}

	return out, nil
}

// ListOSVAdvisoriesForPackages implements OSVMirror.
func (p *OSVStore) ListOSVAdvisoriesForPackages(
	ctx context.Context, ecosystem string, namesNorm []string,
) (map[string][]models.OSVAdvisoryMatch, error) {
	if len(namesNorm) == 0 {
		return nil, nil //nolint:nilnil // nothing asked, nothing found
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT af.name_norm, a.id, a.modified, a.published, a.summary, a.severity, a.db_severity, a.cwe_ids, a.refs,
		       ARRAY(SELECT al.alias FROM osv_advisory_aliases al WHERE al.advisory_id = a.id AND al.alias <> a.id ORDER BY al.alias),
		       af.name, af.ranges, af.versions
		FROM osv_affected af JOIN osv_advisories a ON a.id = af.advisory_id
		WHERE af.ecosystem = $1 AND af.name_norm = ANY($2) AND a.withdrawn IS NULL
		ORDER BY af.name_norm, a.id, af.id`, ecosystem, namesNorm)
	if err != nil {
		return nil, fmt.Errorf("list osv advisories: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]models.OSVAdvisoryMatch)

	for rows.Next() {
		var (
			norm      string
			adv       models.OSVAdvisory
			published sql.NullTime
			aff       = models.OSVAffected{Ecosystem: ecosystem}
		)

		if err := rows.Scan(
			&norm,
			&adv.ID,
			&adv.Modified,
			&published,
			&adv.Summary,
			&adv.Severity,
			&adv.DBSeverity,
			stringArray{
				&adv.CWEIDs,
			},
			stringArray{&adv.Refs},
			stringArray{&adv.Aliases},
			&aff.Name,
			&aff.Ranges,
			stringArray{&aff.Versions},
		); err != nil {
			return nil, fmt.Errorf("scan osv advisory: %w", err)
		}

		if published.Valid {
			adv.Published = &published.Time
		}

		aff.NameNorm = norm

		list := out[norm]
		if n := len(list); n > 0 && list[n-1].Advisory.ID == adv.ID {
			list[n-1].Affected = append(list[n-1].Affected, aff)
		} else {
			out[norm] = append(list, models.OSVAdvisoryMatch{Advisory: adv, Affected: []models.OSVAffected{aff}})
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list osv advisories: %w", err)
	}

	return out, nil
}

// ListStoredPackages implements OSVMirror.
func (p *OSVStore) ListStoredPackages(
	ctx context.Context, format string, after *models.StoredPackage, limit int,
) ([]models.StoredPackage, error) {
	// The proxy cache is left out: it holds fetched files, not stored packages.
	union, formatCond := hostedBranch(), "pk.format = $5"
	if format == dockerFmt {
		union, formatCond = dockerBranch(), "$5::text = '"+dockerFmt+"'"
	}

	var repoID, name, version *string
	if after != nil {
		repoID, name, version = &after.RepositoryID, &after.Name, &after.Version
	}

	query := fmt.Sprintf( //nolint:gosec // G201: the branch is built from constants
		`SELECT n.repository_id::text, n.name, n.version, (array_agg(n.id::text ORDER BY n.id::text))[1], max(n.created_at)
		FROM (%s) n
		JOIN repositories r ON r.id = n.repository_id AND r.deleted_at IS NULL
		WHERE n.name <> '' AND n.version <> ''
		  AND ($1::uuid IS NULL OR (n.repository_id, n.name, n.version) > ($1::uuid, $2::text, $3::text))
		GROUP BY n.repository_id, n.name, n.version
		ORDER BY n.repository_id, n.name, n.version
		LIMIT $4`, union.narrow(formatCond))

	rows, err := p.db.QueryContext(ctx, query, repoID, name, version, limit, format)
	if err != nil {
		return nil, fmt.Errorf("list stored packages: %w", err)
	}
	defer rows.Close()

	var pkgs []models.StoredPackage

	for rows.Next() {
		var sp models.StoredPackage
		if err := rows.Scan(&sp.RepositoryID, &sp.Name, &sp.Version, &sp.ArtifactID, &sp.AddedAt); err != nil {
			return nil, fmt.Errorf("scan stored package: %w", err)
		}

		pkgs = append(pkgs, sp)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list stored packages: %w", err)
	}

	return pkgs, nil
}

// GetOSVScanResults implements OSVMirror.
func (p *OSVStore) GetOSVScanResults(ctx context.Context, pkgs []models.StoredPackage) (map[string]*models.VulnScanResult, error) {
	out := make(map[string]*models.VulnScanResult, len(pkgs))
	if len(pkgs) == 0 {
		return out, nil
	}

	repos := make([]string, len(pkgs))
	paths := make([]string, len(pkgs))

	for i := range pkgs {
		repos[i], paths[i] = pkgs[i].RepositoryID, pkgs[i].ArtifactID
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT `+vulnResultColumns+`
		FROM unnest($1::uuid[], $2::text[]) AS want(repository_id, artifact_path)
		JOIN artifact_refs ref ON ref.repository_id = want.repository_id AND ref.artifact_path = want.artifact_path
		JOIN scan_jobs sj ON sj.artifact_ref_id = ref.id AND sj.scan_type = 'vuln' AND sj.scanner = 'osv'
		JOIN vuln_results vr ON vr.scan_job_id = sj.id`, repos, paths)
	if err != nil {
		return nil, fmt.Errorf("get osv scan results: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		res, err := scanVulnResult(rows)
		if err != nil {
			return nil, fmt.Errorf("scan osv scan result: %w", err)
		}

		out[OSVResultKey(res.RepositoryID, res.ArtifactID)] = res
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get osv scan results: %w", err)
	}

	return out, nil
}

// SaveOSVScanResult implements OSVMirror.
func (p *OSVStore) SaveOSVScanResult(ctx context.Context, result *models.VulnScanResult) error {
	vulns := result.Vulnerabilities
	if vulns == nil {
		vulns = []models.Vulnerability{}
	}

	vulnsJSON, err := json.Marshal(vulns)
	if err != nil {
		return fmt.Errorf("marshal vulnerabilities: %w", err)
	}

	err = p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, completed_at)
			SELECT ref.id, 'vuln', 'osv', 'completed', now() FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = 'completed', error_message = NULL, completed_at = now(), updated_at = now()
			RETURNING id
		)
		INSERT INTO vuln_results (scan_job_id, vulnerabilities, vuln_count, critical_count, high_count, medium_count, low_count,
			package_name, package_version, ecosystem)
		SELECT job.id, $3, $4, $5, $6, $7, $8, $9, $10, $11 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			vulnerabilities = EXCLUDED.vulnerabilities, vuln_count = EXCLUDED.vuln_count,
			critical_count = EXCLUDED.critical_count, high_count = EXCLUDED.high_count,
			medium_count = EXCLUDED.medium_count, low_count = EXCLUDED.low_count,
			package_name = EXCLUDED.package_name, package_version = EXCLUDED.package_version, ecosystem = EXCLUDED.ecosystem
		RETURNING id`,
		result.RepositoryID, result.ArtifactID, vulnsJSON, result.VulnCount, result.CriticalCount, result.HighCount,
		result.MediumCount, result.LowCount, result.PackageName, result.PackageVersion, result.Ecosystem,
	).Scan(&result.ID)
	if err != nil {
		return fmt.Errorf("save osv scan result: %w", err)
	}

	return nil
}

var errTextArrayType = errors.New("scan text[]: unsupported type")

// stringArray scans a Postgres text[] into a []string.
type stringArray struct{ dst *[]string }

// Scan implements sql.Scanner for the text[] text form "{a,b}" and for the
// []string / []any forms drivers may return.
func (s stringArray) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*s.dst = nil
	case []string:
		*s.dst = v
	case []any:
		out := make([]string, len(v))
		for i, e := range v {
			out[i] = fmt.Sprint(e)
		}

		*s.dst = out
	case string:
		*s.dst = parsePGTextArray(v)
	case []byte:
		*s.dst = parsePGTextArray(string(v))
	default:
		return fmt.Errorf("%w: %T", errTextArrayType, src)
	}

	return nil
}

// parsePGTextArray parses the text form of a one-dimensional Postgres text[].
func parsePGTextArray(s string) []string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil
	}

	s = s[1 : len(s)-1]
	if s == "" {
		return nil
	}

	var (
		out    []string
		cur    strings.Builder
		quoted bool
		inQ    bool
	)

	flush := func() {
		v := cur.String()
		if !quoted && v == "NULL" {
			v = ""
		}

		out = append(out, v)
		cur.Reset()

		quoted = false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]

		switch {
		case inQ && c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '"':
			inQ = !inQ
			quoted = true
		case c == ',' && !inQ:
			flush()
		default:
			cur.WriteByte(c)
		}
	}

	flush()

	return out
}

// ListOSVAdvisoryIDs implements OSVMirror.
func (p *OSVStore) ListOSVAdvisoryIDs(ctx context.Context, ecosystem string) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT advisory_id FROM osv_affected WHERE ecosystem = $1`, ecosystem)
	if err != nil {
		return nil, fmt.Errorf("list osv advisory ids: %w", err)
	}
	defer rows.Close()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan osv advisory id: %w", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list osv advisory ids: %w", err)
	}

	return ids, nil
}

// RemoveOSVAdvisories implements OSVMirror.
func (p *OSVStore) RemoveOSVAdvisories(ctx context.Context, ecosystem string, ids []string) ([]models.OSVPackageKey, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin osv removal transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`DELETE FROM osv_affected WHERE ecosystem = $1 AND advisory_id = ANY($2) RETURNING ecosystem, name_norm`, ecosystem, ids)
	if err != nil {
		return nil, fmt.Errorf("remove osv affected packages: %w", err)
	}
	defer rows.Close()

	seen := map[models.OSVPackageKey]bool{}

	var keys []models.OSVPackageKey

	for rows.Next() {
		var k models.OSVPackageKey
		if err := rows.Scan(&k.Ecosystem, &k.NameNorm); err != nil {
			return nil, fmt.Errorf("scan removed osv package: %w", err)
		}

		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("remove osv affected packages: %w", err)
	}

	// The result set must be read to the end before the next statement runs.
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("remove osv affected packages: %w", err)
	}

	// An advisory left without an affected package matches nothing in any
	// ecosystem: delete it, its aliases cascade.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM osv_advisories a
		WHERE a.id = ANY($1) AND NOT EXISTS (SELECT 1 FROM osv_affected af WHERE af.advisory_id = a.id)`, ids); err != nil {
		return nil, fmt.Errorf("remove osv advisories: %w", err)
	}

	if len(keys) > 0 {
		if _, err := tx.ExecContext(ctx, osvMarkPendingSQL, []string{ecosystem}); err != nil {
			return nil, fmt.Errorf("flag osv re-match pending: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit osv removal: %w", err)
	}

	return keys, nil
}
