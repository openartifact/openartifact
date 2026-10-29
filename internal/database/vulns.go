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

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/exploit"
)

// VulnStore owns vulnerability scan results, the vulnerability overview and the security findings views.
type VulnStore struct{ *core }

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
func (p *VulnStore) ListVulnScanOverview(ctx context.Context, params models.VulnScanListParams) (*models.VulnScanOverviewPage, error) {
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
func (p *VulnStore) VulnScanOverviewSummary(ctx context.Context, repoName string) (*models.VulnScanSummary, error) {
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

// vulnScanStatusToJob/FromJob map VulnScanResult.ScanStatus's "scanning" onto
// scan_jobs' "running" (the CHECK-constrained vocabulary), and back.
func vulnScanStatusToJob(status string) string {
	if status == "scanning" {
		return "running"
	}

	return status
}

// CreateVulnScanResult creates or updates a vulnerability scan result.
func (p *VulnStore) CreateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	vulnsJSON, err := json.Marshal(result.Vulnerabilities)
	if err != nil {
		return fmt.Errorf("failed to marshal vulnerabilities: %w", err)
	}

	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	err = p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
			SELECT ref.id, 'vuln', $3, $4, NULLIF($5, ''), $6 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message,
				completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id
		)
		INSERT INTO vuln_results (scan_job_id, vulnerabilities, vuln_count, critical_count, high_count, medium_count, low_count,
			quarantined, package_name, package_version, ecosystem, failed_scanners, kev_count, max_epss)
		SELECT job.id, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			vulnerabilities = EXCLUDED.vulnerabilities, vuln_count = EXCLUDED.vuln_count,
			critical_count = EXCLUDED.critical_count, high_count = EXCLUDED.high_count,
			medium_count = EXCLUDED.medium_count, low_count = EXCLUDED.low_count,
			quarantined = EXCLUDED.quarantined, package_name = EXCLUDED.package_name,
			package_version = EXCLUDED.package_version, ecosystem = EXCLUDED.ecosystem,
			failed_scanners = EXCLUDED.failed_scanners, kev_count = EXCLUDED.kev_count, max_epss = EXCLUDED.max_epss
		RETURNING id`,
		result.RepositoryID, result.ArtifactID, result.Scanner, vulnScanStatusToJob(result.ScanStatus), result.ScanError, scannedAt,
		vulnsJSON, result.VulnCount, result.CriticalCount, result.HighCount, result.MediumCount, result.LowCount,
		result.Quarantined, result.PackageName, result.PackageVersion, result.Ecosystem, nonNilStrings(result.FailedScanners),
		toInt32(result.KEVCount), result.MaxEPSS,
	).Scan(&result.ID)
	if err != nil {
		return fmt.Errorf("failed to create vuln scan result: %w", err)
	}

	return nil
}

// GetVulnScanResult gets a vulnerability scan result by repo and artifact
// ID. scan_jobs' UNIQUE(artifact_ref_id, scan_type, scanner) allows more
// than one scanner (osv/trivy/grype/...) to have scanned the same artifact,
// so this orders by most-recently-created and takes the first row instead
// of letting QueryRowContext pick an arbitrary one nondeterministically.
func (p *VulnStore) GetVulnScanResult(ctx context.Context, repoID, artifactID string) (*models.VulnScanResult, error) {
	result, err := scanVulnResult(p.db.QueryRowContext(ctx, `
		SELECT `+vulnResultColumns+`
		FROM vuln_results vr
		JOIN scan_jobs sj ON sj.id = vr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'vuln' AND ref.repository_id = $1 AND ref.artifact_path = $2
		ORDER BY sj.created_at DESC
		LIMIT 1`,
		repoID, artifactID,
	))

	return orNotFound(result, err, "get vuln scan result")
}

// UpdateVulnScanResult updates a vulnerability scan result.
func (p *VulnStore) UpdateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	vulnsJSON, err := json.Marshal(result.Vulnerabilities)
	if err != nil {
		return fmt.Errorf("failed to marshal vulnerabilities: %w", err)
	}

	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	_, err = p.db.ExecContext(ctx, `
		WITH res AS (
			UPDATE vuln_results SET vulnerabilities = $1, vuln_count = $2, critical_count = $3,
			       high_count = $4, medium_count = $5, low_count = $6, quarantined = $7,
			       failed_scanners = $13, kev_count = $14, max_epss = $15
			WHERE id = $11
			RETURNING scan_job_id
		)
		UPDATE scan_jobs sj SET scanner = $8, status = $9, error_message = NULLIF($10, ''), completed_at = $12, updated_at = now()
		FROM res WHERE sj.id = res.scan_job_id`,
		vulnsJSON, result.VulnCount, result.CriticalCount, result.HighCount, result.MediumCount, result.LowCount, result.Quarantined,
		result.Scanner, vulnScanStatusToJob(result.ScanStatus), result.ScanError, result.ID, scannedAt,
		nonNilStrings(result.FailedScanners), toInt32(result.KEVCount), result.MaxEPSS,
	)
	if err != nil {
		return fmt.Errorf("failed to update vuln scan result: %w", err)
	}

	return nil
}

// ListVulnScanResults lists all vulnerability scan results for a repository.
func (p *VulnStore) ListVulnScanResults(ctx context.Context, repoID string, vulnerableOnly bool) ([]*models.VulnScanResult, error) {
	query := `
		SELECT ` + vulnResultColumns + `
		FROM vuln_results vr
		JOIN scan_jobs sj ON sj.id = vr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'vuln' AND ref.repository_id = $1`
	if vulnerableOnly {
		query += ` AND vr.vuln_count > 0`
	}

	query += ` ORDER BY sj.created_at DESC`

	rows, err := p.db.QueryContext(ctx, query, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list vuln scan results: %w", err)
	}
	defer rows.Close()

	results := make([]*models.VulnScanResult, 0)

	for rows.Next() {
		result, err := scanVulnResult(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan vuln scan result: %w", err)
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// QuarantineVulnArtifact updates the quarantine status of a vulnerable artifact.
func (p *VulnStore) QuarantineVulnArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	_, err := p.db.ExecContext(ctx, `
		WITH target AS (
			SELECT vr.id AS result_id, sj.id AS job_id
			FROM vuln_results vr
			JOIN scan_jobs sj ON sj.id = vr.scan_job_id
			JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
			WHERE sj.scan_type = 'vuln' AND ref.repository_id = $2 AND ref.artifact_path = $3
		), upd1 AS (
			UPDATE vuln_results SET quarantined = $1
			WHERE id = (SELECT result_id FROM target)
			RETURNING id
		)
		UPDATE scan_jobs SET updated_at = now() WHERE id = (SELECT job_id FROM target)`,
		quarantined, repoID, artifactID,
	)
	if err != nil {
		return fmt.Errorf("failed to quarantine vuln artifact: %w", err)
	}

	return nil
}

// ErrInvalidFindingFilter is returned by ListSecurityFindings for a type,
// severity or status it does not know, and for a page size below 1.
var ErrInvalidFindingFilter = errors.New("invalid security finding filter")

// noScanTime is what the security queries return for "no scan": the Unix epoch.
// No scan finished then, so it never collides with a real time.
//
//nolint:gochecknoglobals // immutable sentinel
var noScanTime = time.Unix(0, 0)

// scanTimePtr maps a security query time to nil when it is the noScanTime sentinel.
func scanTimePtr(t time.Time) *time.Time {
	if !t.After(noScanTime) {
		return nil
	}

	return &t
}

// severityRanks maps a severity label to the rank the security_results view uses.
//
//nolint:gochecknoglobals // immutable lookup table
var severityRanks = map[string]int32{
	models.FindingSeverityCritical: 4,
	models.FindingSeverityHigh:     3,
	models.FindingSeverityMedium:   2,
	models.FindingSeverityLow:      1,
}

// vexStatusByRank is the inverse of the vex_rank column of the view.
//
//nolint:gochecknoglobals // immutable lookup table
var vexStatusByRank = map[int32]string{
	1: models.VEXAffected,
	2: models.VEXUnderInvestigation,
	3: models.VEXFixed,
	4: models.VEXNotAffected,
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// scopeProjects returns the project ids as a non-nil slice (a nil slice would
// bind as SQL NULL, which matches nothing in ANY()).
func scopeProjects(s models.FindingScope) []string {
	if s.ProjectIDs == nil {
		return []string{}
	}

	return s.ProjectIDs
}

// validFindingParams reports whether p only holds values the view knows.
func validFindingParams(p models.FindingListParams) (*int32, bool) {
	if p.Limit < 1 || len(p.Types) == 0 {
		return nil, false
	}

	switch p.Sort {
	case "", models.FindingSortSeverity, models.FindingSortPriority:
	default:
		return nil, false
	}

	for _, t := range p.Types {
		switch t {
		case models.FindingVuln, models.FindingLicense, models.FindingSecret, models.FindingAI:
		default:
			return nil, false
		}
	}

	switch p.Status {
	case "", models.FindingStatusOpen, models.FindingStatusQuarantined, models.FindingStatusSuppressed:
	default:
		return nil, false
	}

	if p.Severity == "" {
		return nil, true
	}

	rank, ok := severityRanks[p.Severity]
	if !ok {
		return nil, false
	}

	return &rank, true
}

// sortName is the order of a list: severity unless priority was asked for.
func sortName(s string) string {
	if s == models.FindingSortPriority {
		return s
	}

	return models.FindingSortSeverity
}

// ListSecurityFindings returns one keyset page of the findings of every
// requested type, worst first (by severity, or by exploit priority with
// Sort priority), then newest scan, then id.
func (p *VulnStore) ListSecurityFindings(ctx context.Context, params models.FindingListParams) (*models.FindingPage, error) {
	rank, ok := validFindingParams(params)
	if !ok {
		return nil, ErrInvalidFindingFilter
	}

	arg := dbsqlc.ListSecurityFindingsParams{
		Kinds:         params.Types,
		ScopeAll:      params.Scope.All,
		ScopeProjects: scopeProjects(params.Scope),
		Repo:          optionalText(params.Repo),
		Project:       optionalText(params.Project),
		SeverityRank:  rank,
		Status:        optionalText(params.Status),
		PageLimit:     int32(params.Limit + 1), //nolint:gosec // G115: the page size is bounded by the handler
		SortPriority:  params.Sort == models.FindingSortPriority,
		EpssThreshold: params.EPSSThreshold,
		Kev:           params.KEV,
		EpssMin:       params.EPSSMin,
		// The cursor parameters are cast even when unused; a placeholder keeps the uuid cast valid.
		AfterID: "00000000-0000-0000-0000-000000000000",
		AfterAt: noScanTime,
	}

	if params.PriorityRank != nil {
		pr := int32(*params.PriorityRank) //nolint:gosec // G115: a rank is 0..6
		arg.PriorityRank = &pr
	}

	if params.Q != "" {
		like := "%" + escapeLike(params.Q) + "%"
		arg.Q = &like
	}

	if c := params.After; c != nil {
		arg.HasCursor = true
		arg.AfterRank = int32(c.Rank) //nolint:gosec // G115: a rank is 0..6
		arg.AfterAt = c.ScannedAt
		arg.AfterID = c.ID
	}

	rows, err := p.q.ListSecurityFindings(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("list security findings: %w", err)
	}

	page := &models.FindingPage{Findings: make([]*models.Finding, 0, len(rows))}

	for i, r := range rows {
		if i == params.Limit {
			last := page.Findings[len(page.Findings)-1]
			page.Next = &models.FindingCursor{
				Sort: sortName(params.Sort), Rank: last.SortRank, ScannedAt: last.ScannedAt, ID: last.ID,
			}

			break
		}

		page.Findings = append(page.Findings, findingFromRow(r))
	}

	if err := p.resolveFindingPackages(ctx, page.Findings); err != nil {
		return nil, err
	}

	return page, nil
}

// resolveFindingPackages replaces the package name of findings recorded against
// a package row id (scans started from a package page use the row id as
// artifact_id, and the view then has nothing but that id to show) with the
// row's name and version. An id that no longer resolves (purged package) is
// blanked: the API never returns a bare id as a package name.
func (p *VulnStore) resolveFindingPackages(ctx context.Context, findings []*models.Finding) error {
	var ids []string

	for _, f := range findings {
		if f.PackageName == f.ArtifactID && isUUID(f.ArtifactID) {
			ids = append(ids, f.ArtifactID)
		}
	}

	if len(ids) == 0 {
		return nil
	}

	rows, err := p.q.ResolveComponentNames(ctx, ids)
	if err != nil {
		return fmt.Errorf("resolve finding packages: %w", err)
	}

	type key struct{ repo, id string }

	names := make(map[key]dbsqlc.ResolveComponentNamesRow, len(rows))
	for _, r := range rows {
		names[key{r.RepositoryID, r.ID}] = r
	}

	for _, f := range findings {
		if f.PackageName != f.ArtifactID || !isUUID(f.ArtifactID) {
			continue
		}

		r, ok := names[key{f.RepositoryID, f.ArtifactID}]
		if !ok {
			f.PackageName, f.PackageVersion = "", ""

			continue
		}

		f.PackageName, f.PackageVersion = r.Name, r.Version
	}

	return nil
}

func findingFromRow(r dbsqlc.ListSecurityFindingsRow) *models.Finding {
	return &models.Finding{
		ID:             r.ID,
		Type:           r.Kind,
		RepositoryID:   r.RepositoryID,
		Repository:     r.RepositoryName,
		Format:         r.RepositoryFormat,
		ArtifactID:     r.ArtifactID,
		PackageName:    r.PackageName,
		PackageVersion: r.PackageVersion,
		Scanner:        r.Scanner,
		ScanStatus:     r.ScanStatus,
		Critical:       int(r.Critical),
		High:           int(r.High),
		Medium:         int(r.Medium),
		Low:            int(r.Low),
		Quarantined:    r.Quarantined,
		Status:         r.Status,
		VEXStatus:      vexStatusByRank[r.VexRank],
		SeverityRank:   int(r.SeverityRank),
		KEVCount:       int(r.KevCount),
		MaxEPSS:        r.MaxEpss,
		SortRank:       int(r.SortRank),
		Title:          r.Title,
		ScannedAt:      r.ScannedAt,
	}
}

// SecurityFindingsSummary counts the findings of the filter's scope per type and
// how many of its repositories were scanned.
func (p *VulnStore) SecurityFindingsSummary(ctx context.Context, f models.FindingFilter) (*models.FindingsSummary, error) {
	rows, err := p.q.SecurityFindingsSummary(ctx, dbsqlc.SecurityFindingsSummaryParams{
		ScopeAll:      f.Scope.All,
		ScopeProjects: scopeProjects(f.Scope),
		Repo:          optionalText(f.Repo),
		Project:       optionalText(f.Project),
	})
	if err != nil {
		return nil, fmt.Errorf("security findings summary: %w", err)
	}

	out := &models.FindingsSummary{ByType: map[string]models.FindingTypeSummary{}}

	for _, t := range models.FindingTypes() {
		out.ByType[t] = models.FindingTypeSummary{}
	}

	for _, r := range rows {
		out.ByType[r.Kind] = models.FindingTypeSummary{
			Total: int(r.Total), Critical: int(r.Critical), High: int(r.High),
			Medium: int(r.Medium), Low: int(r.Low), Quarantined: int(r.Quarantined),
		}
	}

	cov, err := p.q.SecurityScanCoverage(ctx, dbsqlc.SecurityScanCoverageParams{
		ScopeAll:      f.Scope.All,
		ScopeProjects: scopeProjects(f.Scope),
		Repo:          optionalText(f.Repo),
		Project:       optionalText(f.Project),
	})
	if err != nil {
		return nil, fmt.Errorf("security scan coverage: %w", err)
	}

	out.TotalRepositories = int(cov.TotalRepositories)
	out.ScannedRepositories = int(cov.ScannedRepositories)
	out.LastScanAt = scanTimePtr(cov.LastScanAt)

	return out, nil
}

// SecurityHealthStats returns the instance-wide scan queue and coverage numbers.
func (p *VulnStore) SecurityHealthStats(ctx context.Context) (*models.SecurityHealthStats, error) {
	r, err := p.q.SecurityHealthStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("security health stats: %w", err)
	}

	return &models.SecurityHealthStats{
		ScansPending:          int(r.ScansPending),
		ScansFailed24h:        int(r.ScansFailed24h),
		LastScanAt:            scanTimePtr(r.LastScanAt),
		UnscannedRepositories: int(r.UnscannedRepositories),
	}, nil
}

// RepoSecuritySummary returns the counts-only security state of a repository,
// narrowed to a package (and version) when given.
func (p *VulnStore) RepoSecuritySummary(ctx context.Context, repoID, packageName, version string) (*models.RepoSecuritySummary, error) {
	r, err := p.q.RepoSecuritySummary(ctx, dbsqlc.RepoSecuritySummaryParams{
		RepositoryID:   repoID,
		PackageName:    optionalText(packageName),
		PackageVersion: optionalText(version),
	})
	if err != nil {
		return nil, fmt.Errorf("repository security summary: %w", err)
	}

	return &models.RepoSecuritySummary{
		ScannedAt:               scanTimePtr(r.ScannedAt),
		VulnCritical:            int(r.VulnCritical),
		VulnHigh:                int(r.VulnHigh),
		VulnMedium:              int(r.VulnMedium),
		VulnLow:                 int(r.VulnLow),
		LicenseViolations:       int(r.LicenseViolations),
		SecretCount:             int(r.SecretCount),
		AIQuarantineRecommended: int(r.AiQuarantineRecommended),
		AIReviewRequired:        int(r.AiReviewRequired),
		Quarantined:             int(r.Quarantined),
	}, nil
}
