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
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// nonNilStrings returns s, or an empty slice for nil: the NOT NULL text[]
// columns reject the NULL a nil slice is sent as.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// vulnResultColumns is the column list scanVulnResult reads. Queries select it
// FROM vuln_results vr JOIN scan_jobs sj JOIN artifact_refs ref.
const vulnResultColumns = `vr.id, ref.repository_id, ref.artifact_path, vr.package_name, vr.package_version, vr.ecosystem,
	       sj.scanner, sj.status, vr.vulnerabilities, vr.vuln_count, vr.critical_count, vr.high_count, vr.medium_count, vr.low_count,
	       vr.quarantined, sj.error_message, vr.failed_scanners, sj.completed_at, sj.created_at, sj.updated_at,
	       vr.kev_count, vr.max_epss`

// nullStringPtr returns a pointer to the string of a valid NullString, or nil.
func nullStringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}

	return &ns.String
}

// orNotFound maps sql.ErrNoRows to a nil result with a nil error, and wraps any
// other error as "failed to <action>".
func orNotFound[T any](v *T, err error, action string) (*T, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", action, err)
	}

	return v, nil
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanVulnResult reads one row selected with vulnResultColumns.
func scanVulnResult(row rowScanner) (*models.VulnScanResult, error) {
	result := &models.VulnScanResult{}

	var (
		scannedAt sql.NullTime
		errMsg    sql.NullString
		vulnsRaw  []byte
		jobStatus string
		maxEPSS   sql.NullFloat64
	)

	if err := row.Scan(
		&result.ID,
		&result.RepositoryID,
		&result.ArtifactID,
		&result.PackageName,
		&result.PackageVersion,
		&result.Ecosystem,
		&result.Scanner,
		&jobStatus,
		&vulnsRaw,
		&result.VulnCount,
		&result.CriticalCount,
		&result.HighCount,
		&result.MediumCount,
		&result.LowCount,
		&result.Quarantined,
		&errMsg,
		&result.FailedScanners,
		&scannedAt,
		&result.CreatedAt,
		&result.UpdatedAt,
		&result.KEVCount,
		&maxEPSS,
	); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	if maxEPSS.Valid {
		result.MaxEPSS = &maxEPSS.Float64
	}

	result.ScanStatus = vulnScanStatusFromJob(jobStatus)
	result.ScanError = errMsg.String

	if scannedAt.Valid {
		result.ScannedAt = &scannedAt.Time
	}

	if len(vulnsRaw) > 0 {
		if err := json.Unmarshal(vulnsRaw, &result.Vulnerabilities); err != nil {
			return nil, fmt.Errorf("failed to unmarshal vulnerabilities: %w", err)
		}
	}

	return result, nil
}

// licenseResultColumns is the column list scanLicenseResult reads. Queries
// select it FROM license_results lr JOIN scan_jobs sj JOIN artifact_refs ref.
const licenseResultColumns = `lr.id, ref.repository_id, ref.artifact_path, lr.licenses, lr.spdx_expressions,
	       sj.status, sj.error_message, lr.quarantined, lr.quarantine_approved, sj.completed_at, sj.created_at, sj.updated_at,
	       lr.unresolved_licenses, lr.license_source`

// scanLicenseResult reads one row selected with licenseResultColumns.
func scanLicenseResult(row rowScanner) (*models.LicenseScanResult, error) {
	result := &models.LicenseScanResult{}

	var (
		scannedAt sql.NullTime
		errMsg    sql.NullString
	)

	if err := row.Scan(&result.ID, &result.RepositoryID, &result.ArtifactID, &result.Licenses, &result.SPDXExpressions,
		&result.ScanStatus, &errMsg, &result.Quarantined, &result.QuarantineApproved, &scannedAt, &result.CreatedAt, &result.UpdatedAt,
		&result.UnresolvedLicenses, &result.LicenseSource); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	result.ScanError = errMsg.String
	if scannedAt.Valid {
		result.ScannedAt = &scannedAt.Time
	}

	return result, nil
}

// collectRows scans every row of rows with scan, closes rows and returns the
// results in order (nil when there are none). scanMsg and listMsg prefix the
// error of a failed scan and of a failed iteration.
func collectRows[T any](rows *sql.Rows, scanMsg, listMsg string, scan func(rowScanner) (T, error)) ([]T, error) {
	defer func() { _ = rows.Close() }()

	var out []T

	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", scanMsg, err)
		}

		out = append(out, v)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", listMsg, err)
	}

	return out, nil
}

// scanRepository reads one row selected with repoColumns.
func scanRepository(row rowScanner) (*models.Repository, error) {
	r := &models.Repository{}
	if err := row.Scan(repoScanDest(r)...); err != nil {
		return nil, err //nolint:wrapcheck // collectRows wraps with the caller's message
	}

	return r, nil
}

// mapRow turns the result of a sqlc single-row query into a model: sql.ErrNoRows
// yields (nil, nil), any other error is wrapped as "failed to <action>".
func mapRow[R, T any](row R, err error, action string, mapf func(R) *T) (*T, error) {
	v, err := orNotFound(&row, err, action)
	if v == nil || err != nil {
		return nil, err
	}

	return mapf(*v), nil
}

// mapSlice converts every element of in with f.
func mapSlice[R, T any](in []R, f func(R) T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[i] = f(v)
	}

	return out
}

// execMatching runs a write statement and returns notFound when it matched no
// row; a failure is wrapped as "failed to <action>".
func (p *PostgresDB) execMatching(ctx context.Context, action string, notFound error, query string, args ...any) error {
	res, err := p.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to %s: %w", action, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to %s: %w", action, err)
	}

	if n == 0 {
		return notFound
	}

	return nil
}

// mapRows is the tail of a sqlc list query: a failure is wrapped as
// "failed to <action>", otherwise every row is converted with mapf.
func mapRows[R, T any](rows []R, err error, action string, mapf func(R) T) ([]T, error) {
	if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", action, err)
	}

	return mapSlice(rows, mapf), nil
}

// mapRowsOrNil is mapRows for lists that are nil, not empty, when there are no rows.
func mapRowsOrNil[R, T any](rows []R, err error, action string, mapf func(R) T) ([]T, error) {
	out, err := mapRows(rows, err, action, mapf)
	if len(out) == 0 {
		return nil, err
	}

	return out, err
}

// scanString reads a single text column.
func scanString(row rowScanner) (string, error) {
	var s string

	return s, row.Scan(&s) //nolint:wrapcheck // collectRows wraps with the caller's message
}

// insertedRow is what an INSERT ... RETURNING id, created_at, updated_at yields;
// the sqlc Create...Row types of such queries convert to it.
type insertedRow struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// finishCreate copies the generated columns of a created row into the model's
// fields, or wraps a failed insert as "failed to <action>".
func finishCreate(row insertedRow, err error, action string, id *string, createdAt, updatedAt *time.Time) error {
	if err != nil {
		return fmt.Errorf("failed to %s: %w", action, err)
	}

	*id, *createdAt, *updatedAt = row.ID, row.CreatedAt, row.UpdatedAt

	return nil
}

// storeRow is the tail of an upsert that returns the stored row: a failure is
// wrapped as "failed to <action>", otherwise the converted row replaces *dst.
func storeRow[R, T any](dst *T, row R, err error, action string, mapf func(R) *T) error {
	if err != nil {
		return fmt.Errorf("failed to %s: %w", action, err)
	}

	*dst = *mapf(row)

	return nil
}
