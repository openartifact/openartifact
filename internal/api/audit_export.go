// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// auditExportPageSize is the number of entries read from the database per
// keyset page and flushed to the client at once: the memory ceiling of an
// export is one page, however long the log is.
const auditExportPageSize = 1000

const (
	auditExportJSONL = "jsonl"
	auditExportCSV   = "csv"
)

// adminAuditExportColumns is the stable, ordered field set of the admin audit
// export (CSV header and JSONL keys). Adding a field is allowed, renaming or
// removing one is a breaking change; docs/operations/monitoring.md lists it.
func adminAuditExportColumns() []string {
	return []string{
		"id", "timestamp", "action", "actor", "actor_type", "actor_id",
		"resource_type", "resource_id", "repository_id", "project_id", "ip", "details",
	}
}

// projectActivityExportColumns is the field set of the project activity
// export. It carries no client address and no user ids.
func projectActivityExportColumns() []string {
	return []string{
		"id", "timestamp", "action", "actor", "actor_type", "message",
		"resource_type", "resource_id", "repository_id",
	}
}

// adminAuditExportRow is one JSONL line of the admin export. Optional columns
// are null when unset; details is the stored JSON object as is.
type adminAuditExportRow struct {
	ID           string          `json:"id"`
	Timestamp    string          `json:"timestamp"`
	Action       string          `json:"action"`
	Actor        string          `json:"actor"`
	ActorType    *string         `json:"actor_type"`
	ActorID      *string         `json:"actor_id"`
	ResourceType *string         `json:"resource_type"`
	ResourceID   *string         `json:"resource_id"`
	RepositoryID *string         `json:"repository_id"`
	ProjectID    *string         `json:"project_id"`
	IP           *string         `json:"ip"`
	Details      json.RawMessage `json:"details"`
}

func newAdminAuditExportRow(l *models.AuditLog) *adminAuditExportRow {
	var details struct {
		Actor string `json:"actor"`
	}

	// A row whose details are not an object just has no actor name.
	_ = json.Unmarshal(l.DetailsJSON, &details)

	raw := l.DetailsJSON
	if !json.Valid(raw) {
		raw = json.RawMessage("null")
	}

	return &adminAuditExportRow{
		ID:           l.ID,
		Timestamp:    l.CreatedAt.UTC().Format(time.RFC3339Nano),
		Action:       l.Action,
		Actor:        details.Actor,
		ActorType:    l.ActorType,
		ActorID:      l.ActorID,
		ResourceType: l.ResourceType,
		ResourceID:   l.ResourceID,
		RepositoryID: l.RepositoryID,
		ProjectID:    l.ProjectID,
		IP:           l.IPAddress,
		Details:      raw,
	}
}

func (r *adminAuditExportRow) record() []string {
	return []string{
		r.ID, r.Timestamp, r.Action, r.Actor, derefString(r.ActorType), derefString(r.ActorID),
		derefString(r.ResourceType), derefString(r.ResourceID), derefString(r.RepositoryID), derefString(r.ProjectID),
		derefString(r.IP), string(r.Details),
	}
}

type projectActivityExportRow struct {
	responses.ProjectActivityEntry
}

func newProjectActivityExportRow(l *models.AuditLog) *projectActivityExportRow {
	e := activityEntry(l)
	e.Timestamp = l.CreatedAt.UTC().Format(time.RFC3339Nano)

	return &projectActivityExportRow{e}
}

func (r *projectActivityExportRow) record() []string {
	return []string{
		r.ID, r.Timestamp, r.Action, r.Actor, derefString(r.ActorType), r.Message,
		derefString(r.ResourceType), derefString(r.ResourceID), derefString(r.RepositoryID),
	}
}

// auditExportRow is a row of either export, rendered as JSONL (through its
// json tags) or as a CSV record in the order of the matching column list.
type auditExportRow interface {
	record() []string
}

// auditExport describes one export: which entries, which columns and how a
// stored entry becomes a row.
type auditExport struct {
	db      database.Database
	log     *logger.Logger
	params  models.AuditListParams
	format  string
	columns []string
	row     func(*models.AuditLog) auditExportRow
	// filePrefix starts the Content-Disposition filename.
	filePrefix string
}

// errAuditExportStreamBroken marks a failure after the response started: the
// status line is already out, so the only honest signal is to abort the
// connection and let the client see a truncated (invalid) transfer.
var errAuditExportStreamBroken = errors.New("audit export interrupted")

// parseAuditExportQuery reads the shared export query: format (jsonl by
// default), actor, action, since and until. It writes a 400 and returns false
// on invalid input.
func parseAuditExportQuery(w http.ResponseWriter, r *http.Request, params *models.AuditListParams) (string, bool) {
	q := r.URL.Query()

	format := q.Get("format")

	switch format {
	case "":
		format = auditExportJSONL
	case auditExportJSONL, auditExportCSV:
	default:
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be jsonl or csv")
		return "", false
	}

	for _, f := range []struct {
		name string
		dst  **time.Time
	}{{"since", &params.Since}, {"until", &params.Until}} {
		raw := q.Get(f.name)
		if raw == "" {
			continue
		}

		t, err := parseAuditExportTime(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", f.name+" must be an RFC 3339 timestamp or a YYYY-MM-DD date (UTC)")
			return "", false
		}

		*f.dst = &t
	}

	if params.Since != nil && params.Until != nil && !params.Since.Before(*params.Until) {
		writeError(w, http.StatusBadRequest, "validation_failed", "since must be before until")
		return "", false
	}

	params.Actor = q.Get("actor")
	params.Action = q.Get("action")

	return format, true
}

// parseAuditExportTime accepts an RFC 3339 timestamp or a bare UTC date
// (midnight at the start of that day).
func parseAuditExportTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}

	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}

	return t, nil
}

var exportNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportFilename is "<prefix>-<UTC timestamp>.<format>" with the prefix
// reduced to filename-safe characters.
func exportFilename(prefix, format string, now time.Time) string {
	prefix = strings.Trim(exportNameUnsafe.ReplaceAllString(prefix, "-"), "-.")
	if prefix == "" {
		prefix = "audit"
	}

	return fmt.Sprintf("%s-%s.%s", prefix, now.UTC().Format("20060102T150405Z"), format)
}

func (e *auditExport) contentType() string {
	if e.format == auditExportCSV {
		return "text/csv; charset=utf-8"
	}

	return "application/x-ndjson"
}

// serve streams the export. Entries are read in keyset pages (newest first,
// the order of the listing), so no COUNT runs and memory stays at one page;
// the response is flushed after every page. The first page is read before any
// header is written so a database failure is still a clean 500. A failure
// later aborts the connection instead of ending the body normally, because a
// silently shortened audit export must not look complete.
func (e *auditExport) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	e.params.Limit = auditExportPageSize
	e.params.NoTotal = true

	page, err := e.db.ListAuditLogs(ctx, e.params)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Errorw("audit export: first page", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		}

		return
	}

	// An export of a large log outlives any fixed write timeout.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", e.contentType())
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": exportFilename(e.filePrefix, e.format, time.Now()),
	}))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	out := bufio.NewWriterSize(w, 64<<10) //nolint:mnd // 64 KiB write buffer
	enc := newExportEncoder(out, e.format, e.columns)

	if err := e.stream(ctx, rc, out, enc, page); err != nil {
		if ctx.Err() == nil {
			e.log.Errorw("audit export interrupted", "error", err)
		}

		panic(http.ErrAbortHandler)
	}
}

func (e *auditExport) stream(ctx context.Context, rc *http.ResponseController, out *bufio.Writer, enc exportEncoder, page *models.AuditPage) error {
	if err := enc.start(); err != nil {
		return err
	}

	for {
		for _, l := range page.Logs {
			if err := enc.write(e.row(l)); err != nil {
				return err
			}
		}

		if err := enc.flush(); err != nil {
			return err
		}

		if err := out.Flush(); err != nil {
			return fmt.Errorf("%w: %w", errAuditExportStreamBroken, err)
		}

		// A flush failure means the client is gone; ignore a writer that
		// cannot flush (tests): the buffered writer already pushed the page.
		_ = rc.Flush()

		if page.Next == nil {
			return nil
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", errAuditExportStreamBroken, err)
		}

		next := e.params
		next.After = page.Next

		var err error

		page, err = e.db.ListAuditLogs(ctx, next)
		if err != nil {
			return fmt.Errorf("%w: %w", errAuditExportStreamBroken, err)
		}
	}
}

// exportEncoder writes rows in one wire format into a buffered writer.
type exportEncoder interface {
	start() error
	write(auditExportRow) error
	flush() error
}

func newExportEncoder(out *bufio.Writer, format string, columns []string) exportEncoder {
	if format == auditExportCSV {
		return &csvExportEncoder{w: csv.NewWriter(out), columns: columns}
	}

	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	return &jsonlExportEncoder{enc: enc}
}

// jsonlExportEncoder emits one compact JSON object per line (json.Encoder
// escapes control characters, so a line never splits inside a value).
type jsonlExportEncoder struct {
	enc *json.Encoder
}

func (j *jsonlExportEncoder) start() error { return nil }

func (j *jsonlExportEncoder) write(r auditExportRow) error {
	return j.enc.Encode(r) //nolint:wrapcheck // buffered write error, reported by the caller
}

func (j *jsonlExportEncoder) flush() error { return nil }

type csvExportEncoder struct {
	w       *csv.Writer
	columns []string
}

func (c *csvExportEncoder) start() error {
	return c.w.Write(c.columns) //nolint:wrapcheck // buffered write error, reported by the caller
}

func (c *csvExportEncoder) write(r auditExportRow) error {
	rec := r.record()
	for i, cell := range rec {
		rec[i] = csvSafeCell(cell)
	}

	return c.w.Write(rec) //nolint:wrapcheck // buffered write error, reported by the caller
}

func (c *csvExportEncoder) flush() error {
	c.w.Flush()

	return c.w.Error() //nolint:wrapcheck // buffered write error, reported by the caller
}

// csvSafeCell neutralises spreadsheet formula injection: a cell that starts
// with =, +, -, @, tab or carriage return would be evaluated by Excel or
// LibreOffice, so it is prefixed with a single quote (OWASP CSV injection
// guidance). The guard changes the exported text of such cells; consumers that
// need the raw value read the JSONL export.
func csvSafeCell(s string) string {
	if s == "" {
		return s
	}

	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}

	return s
}
