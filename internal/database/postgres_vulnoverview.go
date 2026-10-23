// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrInvalidVulnScanFilter is returned by ListVulnScanOverview for a severity,
// status or scanner it does not know, and for a page size below 1.
var ErrInvalidVulnScanFilter = errors.New("invalid vulnerability scan filter")

// vulnOverviewFrom joins a scan result to its job, artifact and repository.
const vulnOverviewFrom = `FROM vuln_results vr
	JOIN scan_jobs sj ON sj.id = vr.scan_job_id
	JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
	JOIN repositories r ON r.id = ref.repository_id
	WHERE sj.scan_type = 'vuln'`

// vulnOverviewSortAt is the keyset time of a scan: completion, else creation.
const vulnOverviewSortAt = `COALESCE(sj.completed_at, sj.created_at)`

// vulnOverviewKeys are the descending sort keys; the id breaks the remaining
// ties ascending.
const vulnOverviewKeys = `vr.critical_count, vr.high_count, vr.medium_count, vr.low_count, ` + vulnOverviewSortAt

// vulnSeverityPredicate returns the predicate "at least one finding of this
// severity or worse", or "" for an unknown severity.
func vulnSeverityPredicate(severity string) string {
	switch severity {
	case "critical":
		return "vr.critical_count > 0"
	case "high":
		return "vr.critical_count + vr.high_count > 0"
	case "medium":
		return "vr.critical_count + vr.high_count + vr.medium_count > 0"
	case "low":
		return "vr.critical_count + vr.high_count + vr.medium_count + vr.low_count > 0"
	}

	return ""
}

// vulnOverviewWhere builds the filter predicates appended to vulnOverviewFrom.
// Values are always bound arguments; the text is built from constant clauses.
func vulnOverviewWhere(params models.VulnScanListParams) (string, []any, error) {
	where := ""
	args := []any{}

	if params.Repo != "" {
		args = append(args, params.Repo)
		where += fmt.Sprintf(" AND r.name = $%d", len(args))
	}

	if params.Severity != "" {
		pred := vulnSeverityPredicate(params.Severity)
		if pred == "" {
			return "", nil, fmt.Errorf("%w: severity %q", ErrInvalidVulnScanFilter, params.Severity)
		}

		where += " AND " + pred
	}

	switch params.Status {
	case "":
	case "completed", "partial", "failed":
		args = append(args, params.Status)
		where += fmt.Sprintf(" AND sj.status = $%d", len(args))
	case "scanning":
		where += " AND sj.status IN ('pending', 'running')"
	default:
		return "", nil, fmt.Errorf("%w: status %q", ErrInvalidVulnScanFilter, params.Status)
	}

	switch params.Scanner {
	case "":
	case "osv", "trivy", "grype":
		// sj.scanner holds one name or several joined by ",".
		args = append(args, params.Scanner)
		where += fmt.Sprintf(" AND $%d = ANY(string_to_array(sj.scanner, ','))", len(args))
	default:
		return "", nil, fmt.Errorf("%w: scanner %q", ErrInvalidVulnScanFilter, params.Scanner)
	}

	if params.Quarantined != nil {
		args = append(args, *params.Quarantined)
		where += fmt.Sprintf(" AND vr.quarantined = $%d", len(args))
	}

	return where, args, nil
}

// vulnOverviewQuery builds the page query and its arguments: worst first
// (critical, high, medium, low counts descending, newest first, id ASC), Limit+1 rows
// so the caller learns whether more follow without a COUNT(*), and never the
// findings payload.
func vulnOverviewQuery(params models.VulnScanListParams) (string, []any, error) {
	if params.Limit < 1 {
		return "", nil, fmt.Errorf("%w: page limit %d", ErrInvalidVulnScanFilter, params.Limit)
	}

	where, args, err := vulnOverviewWhere(params)
	if err != nil {
		return "", nil, err
	}

	if c := params.After; c != nil {
		// All three sort keys descend, so one row comparison expresses "after
		// the cursor"; the equality branch trims the rows tied with it.
		args = append(args, c.CriticalCount, c.HighCount, c.MediumCount, c.LowCount, c.SortAt, c.ID)
		n := len(args)
		where += fmt.Sprintf(` AND ((%[1]s) < ($%[2]d, $%[3]d, $%[4]d, $%[5]d, $%[6]d)
			OR ((%[1]s) = ($%[2]d, $%[3]d, $%[4]d, $%[5]d, $%[6]d) AND vr.id > $%[7]d::uuid))`,
			vulnOverviewKeys, n-5, n-4, n-3, n-2, n-1, n)
	}

	args = append(args, params.Limit+1)
	query := fmt.Sprintf(`SELECT vr.id, ref.repository_id, r.name, r.format, ref.artifact_path, vr.package_name, vr.package_version, vr.ecosystem,
		COALESCE(sj.scanner, ''), sj.status, vr.vuln_count, vr.critical_count, vr.high_count, vr.medium_count, vr.low_count,
		vr.quarantined, sj.error_message, vr.failed_scanners, sj.completed_at, sj.created_at, sj.updated_at, %s
		%s%s
		ORDER BY vr.critical_count DESC, vr.high_count DESC, vr.medium_count DESC, vr.low_count DESC, %s DESC, vr.id
		LIMIT $%d`, vulnOverviewSortAt, vulnOverviewFrom, where, vulnOverviewSortAt, len(args))

	return query, args, nil
}

// ListVulnScanOverview returns one page of vulnerability scans across all
// repositories, worst first. See vulnOverviewQuery.
func (p *PostgresDB) ListVulnScanOverview(ctx context.Context, params models.VulnScanListParams) (*models.VulnScanOverviewPage, error) {
	query, args, err := vulnOverviewQuery(params)
	if err != nil {
		return nil, err
	}

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list vuln scan overview: %w", err)
	}
	defer func() { _ = rows.Close() }()

	scans := make([]*models.VulnScanOverviewRow, 0, params.Limit+1)

	for rows.Next() {
		row, err := scanVulnOverviewRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan vuln scan overview row: %w", err)
		}

		scans = append(scans, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list vuln scan overview: %w", err)
	}

	page := &models.VulnScanOverviewPage{Scans: scans}

	if len(scans) > params.Limit {
		page.Scans = scans[:params.Limit]
		last := page.Scans[params.Limit-1]
		page.Next = &models.VulnScanCursor{
			CriticalCount: last.CriticalCount,
			HighCount:     last.HighCount,
			MediumCount:   last.MediumCount,
			LowCount:      last.LowCount,
			SortAt:        last.SortAt,
			ID:            last.ID,
		}
	}

	return page, nil
}

func scanVulnOverviewRow(row rowScanner) (*models.VulnScanOverviewRow, error) {
	out := &models.VulnScanOverviewRow{}
	res := &out.VulnScanResult

	var (
		scannedAt sql.NullTime
		errMsg    sql.NullString
		jobStatus string
	)

	if err := row.Scan(&res.ID, &res.RepositoryID, &out.RepositoryName, &out.RepositoryFormat, &res.ArtifactID, &res.PackageName, &res.PackageVersion, &res.Ecosystem,
		&res.Scanner, &jobStatus, &res.VulnCount, &res.CriticalCount, &res.HighCount, &res.MediumCount, &res.LowCount,
		&res.Quarantined, &errMsg, &res.FailedScanners, &scannedAt, &res.CreatedAt, &res.UpdatedAt, &out.SortAt); err != nil {
		return nil, err //nolint:wrapcheck // the caller wraps it
	}

	res.ScanStatus = vulnScanStatusFromJob(jobStatus)
	res.ScanError = errMsg.String

	if scannedAt.Valid {
		res.ScannedAt = &scannedAt.Time
	}

	return out, nil
}

// VulnScanOverviewSummary counts the overview's scans in one pass, all of them
// or those of one repository.
func (p *PostgresDB) VulnScanOverviewSummary(ctx context.Context, repoName string) (*models.VulnScanSummary, error) {
	where, args := "", []any{}

	if repoName != "" {
		where, args = " AND r.name = $1", append(args, repoName)
	}

	query := `SELECT count(*),
		count(*) FILTER (WHERE sj.status = 'completed'),
		count(*) FILTER (WHERE sj.status = 'partial'),
		count(*) FILTER (WHERE sj.status = 'failed'),
		count(*) FILTER (WHERE sj.status IN ('pending', 'running')),
		count(*) FILTER (WHERE vr.quarantined),
		count(*) FILTER (WHERE ` + vulnSeverityPredicate("critical") + `),
		count(*) FILTER (WHERE ` + vulnSeverityPredicate("high") + `),
		count(*) FILTER (WHERE ` + vulnSeverityPredicate("medium") + `),
		count(*) FILTER (WHERE ` + vulnSeverityPredicate("low") + `)
		` + vulnOverviewFrom + where

	s := &models.VulnScanSummary{}

	err := p.db.QueryRowContext(ctx, query, args...).Scan(&s.Total, &s.Completed, &s.Partial, &s.Failed, &s.Scanning,
		&s.Quarantined, &s.WithCritical, &s.WithHigh, &s.WithMedium, &s.WithLow)
	if err != nil {
		return nil, fmt.Errorf("vuln scan overview summary: %w", err)
	}

	return s, nil
}
