// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/exploit"
)

// ErrInvalidVulnScanFilter is returned by ListVulnScanOverview for a severity,
// status, scanner, sort or priority it does not know, and for a page size below 1.
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

// vulnRankSQL is the priority rank (6..0, see exploit.Priority.Rank) of a scan
// result row; threshold is the placeholder of the EPSS threshold (a float8).
func vulnRankSQL(threshold string) string {
	return `(CASE WHEN vr.kev_count > 0 THEN 6 WHEN vr.max_epss >= ` + threshold + `::float8 THEN 5
		WHEN vr.critical_count > 0 THEN 4 WHEN vr.high_count > 0 THEN 3
		WHEN vr.medium_count > 0 THEN 2 WHEN vr.low_count > 0 THEN 1 ELSE 0 END)`
}

// vulnThresholdArg is the placeholder of the EPSS threshold: the first argument
// of every query that ranks (see vulnNeedsRank).
const vulnThresholdArg = "$1"

// vulnNeedsRank reports whether the query ranks scans by priority.
func vulnNeedsRank(params models.VulnScanListParams) bool {
	return params.Sort == models.VulnSortPriority || params.Priority != ""
}

// vulnOverviewOrder is the ORDER BY of the severity sort.
const vulnOverviewOrder = `vr.critical_count DESC, vr.high_count DESC, vr.medium_count DESC, vr.low_count DESC, ` +
	vulnOverviewSortAt + ` DESC, vr.id`

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
func vulnOverviewWhere(params models.VulnScanListParams, args []any) (string, []any, error) {
	where := ""

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

	exploitWhere, args, err := vulnExploitWhere(params, args)
	if err != nil {
		return "", nil, err
	}

	return where + exploitWhere, args, nil
}

// vulnExploitWhere builds the predicates of the exploitation filters (kev,
// epss_min, priority). kev=true is "kev_count > 0", the predicate of the
// partial index on that column.
func vulnExploitWhere(params models.VulnScanListParams, args []any) (string, []any, error) {
	where := ""

	if params.KEV != nil {
		if *params.KEV {
			where += " AND vr.kev_count > 0"
		} else {
			where += " AND vr.kev_count = 0"
		}
	}

	if params.EPSSMin != nil {
		args = append(args, *params.EPSSMin)
		where += fmt.Sprintf(" AND vr.max_epss >= $%d", len(args))
	}

	if params.Priority != "" {
		p, ok := exploit.ParsePriority(params.Priority)
		if !ok || p == exploit.PriorityNone {
			return "", nil, fmt.Errorf("%w: priority %q", ErrInvalidVulnScanFilter, params.Priority)
		}

		where += fmt.Sprintf(" AND %s = %d", vulnRankSQL(vulnThresholdArg), p.Rank())
	}

	return where, args, nil
}

// vulnOverviewQuery builds the page query and its arguments: worst first
// (critical, high, medium, low counts descending, newest first, id ASC; with the
// priority sort the priority rank leads), Limit+1 rows so the caller learns
// whether more follow without a COUNT(*), and never the findings payload.
func vulnOverviewQuery(params models.VulnScanListParams) (string, []any, error) {
	if params.Limit < 1 {
		return "", nil, fmt.Errorf("%w: page limit %d", ErrInvalidVulnScanFilter, params.Limit)
	}

	switch params.Sort {
	case "", models.VulnSortSeverity, models.VulnSortPriority:
	default:
		return "", nil, fmt.Errorf("%w: sort %q", ErrInvalidVulnScanFilter, params.Sort)
	}

	args := []any{}
	if vulnNeedsRank(params) {
		args = append(args, params.EPSSThreshold)
	}

	where, args, err := vulnOverviewWhere(params, args)
	if err != nil {
		return "", nil, err
	}

	keys, order := vulnOverviewKeys, vulnOverviewOrder
	if params.Sort == models.VulnSortPriority {
		rank := vulnRankSQL(vulnThresholdArg)
		keys, order = rank+", "+keys, rank+" DESC, "+order
	}

	if c := params.After; c != nil {
		var cursorArgs []any

		if params.Sort == models.VulnSortPriority {
			cursorArgs = append(cursorArgs, c.Rank)
		}

		// Every sort key descends, so one row comparison expresses "after the
		// cursor"; the equality branch trims the rows tied with it.
		cursorArgs = append(cursorArgs, c.CriticalCount, c.HighCount, c.MediumCount, c.LowCount, c.SortAt)
		first := len(args) + 1
		args = append(args, cursorArgs...)
		args = append(args, c.ID)
		last := len(args)

		marks := make([]string, 0, len(cursorArgs))
		for i := first; i < last; i++ {
			marks = append(marks, fmt.Sprintf("$%d", i))
		}

		tuple := "(" + strings.Join(marks, ", ") + ")"
		if params.Sort == models.VulnSortPriority {
			tuple = strings.Replace(tuple, fmt.Sprintf("$%d", first), fmt.Sprintf("$%d::int", first), 1)
		}

		where += fmt.Sprintf(` AND ((%[1]s) < %[2]s OR ((%[1]s) = %[2]s AND vr.id > $%[3]d::uuid))`, keys, tuple, last)
	}

	args = append(args, params.Limit+1)
	query := fmt.Sprintf(
		`SELECT vr.id, ref.repository_id, r.name, r.format, ref.artifact_path, vr.package_name, vr.package_version, vr.ecosystem,
		COALESCE(sj.scanner, ''), sj.status, vr.vuln_count, vr.critical_count, vr.high_count, vr.medium_count, vr.low_count,
		vr.quarantined, sj.error_message, vr.failed_scanners, sj.completed_at, sj.created_at, sj.updated_at, %s,
		vr.kev_count, vr.max_epss
		%s%s
		ORDER BY %s
		LIMIT $%d`,
		vulnOverviewSortAt,
		vulnOverviewFrom,
		where,
		order,
		len(args),
	)

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
			Rank:          vulnRowRank(last, params.EPSSThreshold),
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

// vulnRowRank is the priority rank of a row, as vulnRankSQL computes it.
func vulnRowRank(row *models.VulnScanOverviewRow, threshold float64) int {
	severity := exploit.SeverityRankOfCounts(row.CriticalCount, row.HighCount, row.MediumCount, row.LowCount)

	return exploit.RowPriority(row.KEVCount, row.MaxEPSS, severity, threshold).Rank()
}

func scanVulnOverviewRow(row rowScanner) (*models.VulnScanOverviewRow, error) {
	out := &models.VulnScanOverviewRow{}
	res := &out.VulnScanResult

	var (
		scannedAt sql.NullTime
		errMsg    sql.NullString
		maxEPSS   sql.NullFloat64
		jobStatus string
	)

	if err := row.Scan(
		&res.ID,
		&res.RepositoryID,
		&out.RepositoryName,
		&out.RepositoryFormat,
		&res.ArtifactID,
		&res.PackageName,
		&res.PackageVersion,
		&res.Ecosystem,
		&res.Scanner,
		&jobStatus,
		&res.VulnCount,
		&res.CriticalCount,
		&res.HighCount,
		&res.MediumCount,
		&res.LowCount,
		&res.Quarantined,
		&errMsg,
		&res.FailedScanners,
		&scannedAt,
		&res.CreatedAt,
		&res.UpdatedAt,
		&out.SortAt,
		&res.KEVCount,
		&maxEPSS,
	); err != nil {
		return nil, err //nolint:wrapcheck // the caller wraps it
	}

	res.ScanStatus = vulnScanStatusFromJob(jobStatus)
	res.ScanError = errMsg.String

	if maxEPSS.Valid {
		res.MaxEPSS = &maxEPSS.Float64
	}

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
