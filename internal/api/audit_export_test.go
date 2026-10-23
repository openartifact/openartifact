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
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

var errExportDB = errors.New("export db down")

// exportDB serves audit entries from memory with the keyset semantics of
// PostgresDB.ListAuditLogs (newest first, After is exclusive), on top of the
// project mock for authorization lookups.
type exportDB struct {
	*projectsMockDB

	logs []*models.AuditLog
	// failOnCall makes the n-th (1-based) ListAuditLogs call fail; 0 never.
	failOnCall int
	// onCall runs at the start of every call with its 1-based number.
	onCall func(n int)
	calls  []models.AuditListParams
}

func (d *exportDB) ListAuditLogs(_ context.Context, params models.AuditListParams) (*models.AuditPage, error) {
	d.calls = append(d.calls, params)

	if d.onCall != nil {
		d.onCall(len(d.calls))
	}

	if len(d.calls) == d.failOnCall {
		return nil, errExportDB
	}

	var matched []*models.AuditLog

	for _, l := range d.logs {
		if exportEntryMatches(l, params) {
			matched = append(matched, l)
		}
	}

	page := &models.AuditPage{}

	for _, l := range matched {
		if len(page.Logs) == params.Limit+1 {
			break
		}

		page.Logs = append(page.Logs, l)
	}

	if len(page.Logs) > params.Limit {
		page.Logs = page.Logs[:params.Limit]
		last := page.Logs[params.Limit-1]
		page.Next = &models.AuditCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	return page, nil
}

func exportEntryMatches(l *models.AuditLog, p models.AuditListParams) bool {
	switch {
	case p.After != nil && !afterCursor(l, p.After):
		return false
	case p.Since != nil && l.CreatedAt.Before(*p.Since):
		return false
	case p.Until != nil && !l.CreatedAt.Before(*p.Until):
		return false
	case p.HideProjectTokenEvents && strings.HasPrefix(l.Action, "project.token_"):
		return false
	case p.Action != "" && !strings.Contains(l.Action, p.Action):
		return false
	}

	return true
}

// afterCursor reports whether l sorts strictly after c in (created_at DESC, id).
func afterCursor(l *models.AuditLog, c *models.AuditCursor) bool {
	return l.CreatedAt.Before(c.CreatedAt) || (l.CreatedAt.Equal(c.CreatedAt) && l.ID > c.ID)
}

// exportLogs returns n entries, newest first, one minute apart.
func exportLogs(n int) []*models.AuditLog {
	base := time.Date(2026, 3, 1, 12, 0, 0, 123456000, time.UTC)
	ip := "203.0.113.7"
	logs := make([]*models.AuditLog, n)

	for i := range logs {
		logs[i] = &models.AuditLog{
			ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
			Action:      "repository.created",
			DetailsJSON: json.RawMessage(fmt.Sprintf(`{"actor":"user-%d","n":%d}`, i, i)),
			IPAddress:   &ip,
			CreatedAt:   base.Add(-time.Duration(i) * time.Minute),
		}
	}

	return logs
}

// flushCounter counts how often the handler pushed data to the client.
type flushCounter struct {
	*httptest.ResponseRecorder

	flushes int
}

func (f *flushCounter) Flush() {
	f.flushes++
	f.ResponseRecorder.Flush()
}

// serveExport runs the request and reports whether the handler aborted the
// connection (http.ErrAbortHandler), which is how a broken stream is signalled.
func serveExport(t *testing.T, h http.Handler, ctx context.Context, p *auth.Principal, target string) (*flushCounter, bool) {
	t.Helper()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))
	rec := &flushCounter{ResponseRecorder: httptest.NewRecorder()}
	aborted := false

	func() {
		defer func() {
			if r := recover(); r != nil {
				if r != http.ErrAbortHandler { //nolint:errorlint // panic value is the sentinel itself
					panic(r)
				}

				aborted = true
			}
		}()

		h.ServeHTTP(rec, req)
	}()

	return rec, aborted
}

func newExportAdminRouter(t *testing.T, db *exportDB) http.Handler {
	t.Helper()

	log := newFixTestLogger(t)
	r := chi.NewRouter()
	NewAdminAuditHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

	return r
}

func newExportProjectRouter(t *testing.T, db *exportDB) http.Handler {
	t.Helper()

	log := newFixTestLogger(t)
	r := chi.NewRouter()
	NewProjectsHandler(db, log, auth.NewEvaluator(db, log, false), auth.NewProjectSettings(db, false)).RegisterRoutes(r)

	return r
}

func newExportDB(logs []*models.AuditLog) *exportDB {
	return &exportDB{projectsMockDB: newProjectsMock(), logs: logs}
}

func jsonlKeys(t *testing.T, line string) []string {
	t.Helper()

	dec := json.NewDecoder(strings.NewReader(line))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("line is not an object: %q (%v)", line, err)
	}

	var keys []string

	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatalf("token: %v", err)
		}

		keys = append(keys, k.(string))

		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("value: %v", err)
		}
	}

	return keys
}

func checkFirstAdminRow(t *testing.T, line string, row map[string]any) {
	t.Helper()

	if !slices.Equal(jsonlKeys(t, line), adminAuditExportColumns()) {
		t.Errorf("keys = %v, want %v", jsonlKeys(t, line), adminAuditExportColumns())
	}

	if row["timestamp"] != "2026-03-01T12:00:00.123456Z" || row["actor"] != "user-0" || row["ip"] != "203.0.113.7" || row["actor_id"] != nil {
		t.Errorf("first row = %v", row)
	}

	if d, _ := row["details"].(map[string]any); d["n"] != float64(0) {
		t.Errorf("details = %v", row["details"])
	}
}

func TestAdminAuditExportStreamsEveryEntryInPages(t *testing.T) {
	const total = 2*auditExportPageSize + 345

	db := newExportDB(exportLogs(total))
	rec, aborted := serveExport(t, newExportAdminRouter(t, db), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export?format=jsonl")

	if aborted || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, aborted = %v, body = %s", rec.Code, aborted, rec.Body.String())
	}

	if got := rec.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", got)
	}

	if rec.Header().Get("Content-Length") != "" {
		t.Errorf("a streamed export must not announce a length")
	}

	sc := bufio.NewScanner(rec.Body)
	sc.Buffer(nil, 1<<20)

	n := 0
	for sc.Scan() {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("line %d is not JSON: %v: %s", n, err, sc.Text())
		}

		if want := fmt.Sprintf("00000000-0000-4000-8000-%012d", n); row["id"] != want {
			t.Fatalf("line %d id = %v, want %s (order or duplicate)", n, row["id"], want)
		}

		if n == 0 {
			checkFirstAdminRow(t, sc.Text(), row)
		}

		n++
	}

	if n != total {
		t.Errorf("exported %d entries, want %d", n, total)
	}

	// Three keyset pages, no COUNT: every query is bounded and total-less, and
	// the cursor advances.
	if len(db.calls) != 3 {
		t.Fatalf("queries = %d, want 3", len(db.calls))
	}

	for i, c := range db.calls {
		if c.Limit != auditExportPageSize || !c.NoTotal || c.Offset != 0 || (i == 0) != (c.After == nil) {
			t.Errorf("query %d = %+v", i, c)
		}
	}

	if rec.flushes < 3 {
		t.Errorf("flushes = %d, want one per page", rec.flushes)
	}
}

func TestAdminAuditExportDispositionAndHeaders(t *testing.T) {
	for _, tc := range []struct{ format, ctype, ext string }{
		{"jsonl", "application/x-ndjson", ".jsonl"},
		{"csv", "text/csv; charset=utf-8", ".csv"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			rec, _ := serveExport(t, newExportAdminRouter(t, newExportDB(exportLogs(1))), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export?format="+tc.format)

			disp, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
			if err != nil || disp != "attachment" {
				t.Fatalf("Content-Disposition = %q (%v)", rec.Header().Get("Content-Disposition"), err)
			}

			if !regexp.MustCompile(`^openartifact-audit-\d{8}T\d{6}Z` + regexp.QuoteMeta(tc.ext) + `$`).MatchString(params["filename"]) {
				t.Errorf("filename = %q", params["filename"])
			}

			if rec.Header().Get("Content-Type") != tc.ctype || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("headers = %v", rec.Header())
			}
		})
	}
}

func TestAdminAuditExportDefaultsToJSONL(t *testing.T) {
	rec, _ := serveExport(t, newExportAdminRouter(t, newExportDB(exportLogs(1))), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export")
	if rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestAdminAuditExportCSVIsParseableAndInjectionSafe(t *testing.T) {
	hostile := []string{
		`=HYPERLINK("http://evil.example","x")`, `+1+1`, `-2+3`, `@SUM(A1)`, "\t=1", "\r=1",
		`plain, with comma`, "multi\nline", `quote " inside`, `ünïcode ✓`,
	}

	logs := exportLogs(len(hostile))

	for i, h := range hostile {
		logs[i].Action = "x" + h
		logs[i].ResourceID = &hostile[i]
		logs[i].DetailsJSON, _ = json.Marshal(map[string]string{"actor": h})
	}

	rec, _ := serveExport(t, newExportAdminRouter(t, newExportDB(logs)), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export?format=csv")

	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v\n%s", err, rec.Body.String())
	}

	if len(rows) != len(hostile)+1 || !slices.Equal(rows[0], adminAuditExportColumns()) {
		t.Fatalf("rows = %d, header = %v", len(rows), rows[0])
	}

	actor, resource, details := slices.Index(adminAuditExportColumns(), "actor"), slices.Index(adminAuditExportColumns(), "resource_id"), slices.Index(adminAuditExportColumns(), "details")

	for i, h := range hostile {
		row := rows[i+1]
		if len(row) != len(adminAuditExportColumns()) {
			t.Fatalf("row %d has %d cells", i, len(row))
		}

		// Cells that spreadsheets would evaluate carry a leading quote; every
		// other value round-trips exactly.
		want := h
		if strings.ContainsRune("=+-@\t\r", rune(h[0])) {
			want = "'" + h
		}

		if row[actor] != want || row[resource] != want {
			t.Errorf("row %d actor = %q resource = %q, want %q", i, row[actor], row[resource], want)
		}

		var d map[string]string
		if err := json.Unmarshal([]byte(row[details]), &d); err != nil || d["actor"] != h {
			t.Errorf("row %d details %q: %v", i, row[details], err)
		}

		if c := row[0]; strings.ContainsAny(c[:1], "=+-@") {
			t.Errorf("unguarded id %q", c)
		}
	}
}

func TestAdminAuditExportCSVLeavesUnsetValuesEmpty(t *testing.T) {
	rec, _ := serveExport(t, newExportAdminRouter(t, newExportDB(exportLogs(1))), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export?format=csv")

	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	for _, col := range []string{"actor_type", "actor_id", "resource_type", "resource_id", "repository_id", "project_id"} {
		if v := rows[1][slices.Index(adminAuditExportColumns(), col)]; v != "" {
			t.Errorf("%s = %q, want empty", col, v)
		}
	}
}

func TestAdminAuditExportPassesFiltersAndRange(t *testing.T) {
	db := newExportDB(exportLogs(3))
	target := "/api/v1/admin/audit/export?format=csv&actor=alice&action=auth&since=2026-03-01&until=2026-03-02T00:00:00%2B02:00"

	if rec, _ := serveExport(t, newExportAdminRouter(t, db), context.Background(), testPrincipal(uAdmin1), target); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	p := db.calls[0]
	if p.Actor != "alice" || p.Action != "auth" || p.Since == nil || p.Until == nil {
		t.Fatalf("params = %+v", p)
	}

	if !p.Since.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) || !p.Until.Equal(time.Date(2026, 3, 1, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %v until = %v", p.Since, p.Until)
	}
}

func TestAdminAuditExportRangeSelectsEntries(t *testing.T) {
	db := newExportDB(exportLogs(10)) // 12:00, 11:59, ... 11:51 (UTC)
	// since inclusive 11:55, until exclusive 11:58: entries 11:57, 11:56, 11:55.
	target := "/api/v1/admin/audit/export?since=2026-03-01T11:55:00.123456Z&until=2026-03-01T11:58:00.123456Z"

	rec, _ := serveExport(t, newExportAdminRouter(t, db), context.Background(), testPrincipal(uAdmin1), target)

	var ids []string

	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		var row struct{ ID string }
		_ = json.Unmarshal(sc.Bytes(), &row)
		ids = append(ids, row.ID[len(row.ID)-3:])
	}

	if want := []string{"003", "004", "005"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestAdminAuditExportRejectsBadQuery(t *testing.T) {
	for name, query := range map[string]string{
		"unknown format": "format=xml",
		"bad since":      "since=yesterday",
		"bad until":      "until=2026-13-45",
		"inverted range": "since=2026-03-02&until=2026-03-01",
		"empty range":    "since=2026-03-02&until=2026-03-02",
	} {
		t.Run(name, func(t *testing.T) {
			db := newExportDB(exportLogs(1))
			rec, aborted := serveExport(t, newExportAdminRouter(t, db), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export?"+query)

			if aborted || rec.Code != http.StatusBadRequest || len(db.calls) != 0 {
				t.Errorf("status = %d, queries = %d", rec.Code, len(db.calls))
			}

			if !strings.Contains(rec.Body.String(), "validation_failed") {
				t.Errorf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestAdminAuditExportRequiresAdmin(t *testing.T) {
	for name, p := range map[string]*auth.Principal{"plain user": testPrincipal(uPlain1), "project owner": testPrincipal(uOwner1)} {
		t.Run(name, func(t *testing.T) {
			db := newExportDB(exportLogs(1))
			rec, _ := serveExport(t, newExportAdminRouter(t, db), context.Background(), p, "/api/v1/admin/audit/export")

			if rec.Code != http.StatusForbidden || len(db.calls) != 0 || rec.Body.Len() > 200 {
				t.Errorf("status = %d, queries = %d, body = %s", rec.Code, len(db.calls), rec.Body.String())
			}
		})
	}
}

func TestAdminAuditExportFirstPageFailureIsACleanError(t *testing.T) {
	db := newExportDB(exportLogs(5))
	db.failOnCall = 1

	rec, aborted := serveExport(t, newExportAdminRouter(t, db), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export")

	if aborted || rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, aborted = %v", rec.Code, aborted)
	}

	if strings.Contains(rec.Body.String(), errExportDB.Error()) || rec.Header().Get("Content-Disposition") != "" {
		t.Errorf("leaked detail or started a download: %v %s", rec.Header(), rec.Body.String())
	}
}

func TestAdminAuditExportMidStreamFailureAbortsTheConnection(t *testing.T) {
	db := newExportDB(exportLogs(auditExportPageSize + 10))
	db.failOnCall = 2

	rec, aborted := serveExport(t, newExportAdminRouter(t, db), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export")

	if !aborted {
		t.Fatalf("a failure after the first page must abort the response, got status %d", rec.Code)
	}

	if strings.Contains(rec.Body.String(), errExportDB.Error()) {
		t.Errorf("leaked the database error into the export")
	}
}

func TestAdminAuditExportStopsWhenTheClientGoesAway(t *testing.T) {
	db := newExportDB(exportLogs(4*auditExportPageSize + 1))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The client disconnects while the second page is being read.
	db.onCall = func(n int) {
		if n == 2 {
			cancel()
		}
	}

	_, aborted := serveExport(t, newExportAdminRouter(t, db), ctx, testPrincipal(uAdmin1), "/api/v1/admin/audit/export")

	if !aborted {
		t.Error("a cancelled export must not end like a complete one")
	}

	if len(db.calls) != 2 {
		t.Errorf("queries after cancellation = %d, want it to stop at 2", len(db.calls))
	}
}

func TestAdminAuditExportEmptyLog(t *testing.T) {
	for format, want := range map[string]string{"jsonl": "", "csv": strings.Join(adminAuditExportColumns(), ",") + "\n"} {
		rec, aborted := serveExport(t, newExportAdminRouter(t, newExportDB(nil)), context.Background(), testPrincipal(uAdmin1), "/api/v1/admin/audit/export?format="+format)

		if aborted || rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", format, rec.Code)
		}

		body, _ := io.ReadAll(rec.Body)
		if string(body) != want {
			t.Errorf("%s body = %q, want %q", format, body, want)
		}
	}
}

func projectExportLogs() []*models.AuditLog {
	ip, repo := "203.0.113.9", "11111111-1111-4111-8111-111111111111"
	actorType, actorID := "user", "0f000000-0000-4000-8000-000000000001"
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	return []*models.AuditLog{
		{
			ID: "22222222-2222-4222-8222-222222222221", Action: "project.token_created", ActorType: &actorType, ActorID: &actorID,
			DetailsJSON: json.RawMessage(`{"message":"token ci created","actor":"owner-1"}`), IPAddress: &ip, CreatedAt: created,
		},
		{
			ID: "22222222-2222-4222-8222-222222222222", Action: "project.repository_created", RepositoryID: &repo, ActorType: &actorType, ActorID: &actorID,
			DetailsJSON: json.RawMessage(`{"message":"repository r created, \"quoted\"","actor":"owner-1"}`), IPAddress: &ip, CreatedAt: created.Add(-time.Minute),
		},
	}
}

func TestProjectActivityExportColumnsAndNoClientAddress(t *testing.T) {
	db := newExportDB(projectExportLogs())
	rec, _ := serveExport(t, newExportProjectRouter(t, db), context.Background(), testPrincipal(uMaint1), "/api/v1/projects/acme/activity/export?format=csv")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	if strings.Contains(rec.Body.String(), "203.0.113.9") || strings.Contains(rec.Body.String(), "0f000000-0000-4000-8000-000000000001") {
		t.Errorf("export leaks the client address or a user id: %s", rec.Body.String())
	}

	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(rows[0], projectActivityExportColumns()) || len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}

	if got := rows[2][slices.Index(projectActivityExportColumns(), "message")]; got != `repository r created, "quoted"` {
		t.Errorf("message = %q", got)
	}

	if db.calls[0].ProjectID != testAcmeID {
		t.Errorf("not scoped to the project: %+v", db.calls[0])
	}

	disp, params, _ := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if disp != "attachment" || !regexp.MustCompile(`^openartifact-acme-activity-\d{8}T\d{6}Z\.csv$`).MatchString(params["filename"]) {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestProjectActivityExportJSONLFields(t *testing.T) {
	rec, _ := serveExport(t, newExportProjectRouter(t, newExportDB(projectExportLogs())), context.Background(), testPrincipal(uOwner1), "/api/v1/projects/acme/activity/export")

	line, _, _ := strings.Cut(rec.Body.String(), "\n")
	if !slices.Equal(jsonlKeys(t, line), projectActivityExportColumns()) {
		t.Errorf("keys = %v, want %v", jsonlKeys(t, line), projectActivityExportColumns())
	}

	var row map[string]any
	if err := json.Unmarshal([]byte(line), &row); err != nil || row["timestamp"] != "2026-01-02T03:04:05Z" || row["actor"] != "owner-1" {
		t.Errorf("row = %v (%v)", row, err)
	}
}

func TestProjectActivityExportHidesTokenEventsBelowMaintainer(t *testing.T) {
	for user, hidden := range map[string]bool{uView1: true, uDev1: true, uMaint1: false, uOwner1: false, uAdmin1: false} {
		db := newExportDB(projectExportLogs())
		rec, _ := serveExport(t, newExportProjectRouter(t, db), context.Background(), testPrincipal(user), "/api/v1/projects/acme/activity/export")

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", user, rec.Code)
		}

		if got := strings.Contains(rec.Body.String(), "project.token_created"); got == hidden {
			t.Errorf("%s: token event present = %v, hidden want %v", user, got, hidden)
		}

		if db.calls[0].HideProjectTokenEvents != hidden {
			t.Errorf("%s: HideProjectTokenEvents = %v, want %v", user, db.calls[0].HideProjectTokenEvents, hidden)
		}
	}
}

func TestProjectActivityExportAccess(t *testing.T) {
	for name, tc := range map[string]struct {
		p    *auth.Principal
		path string
		want int
	}{
		"non member gets 404":  {testPrincipal(uNewbie), "/api/v1/projects/acme/activity/export", http.StatusNotFound},
		"unknown project":      {testPrincipal(uOwner1), "/api/v1/projects/nope/activity/export", http.StatusNotFound},
		"scoped token is out":  {scopedPrincipal(uOwner1), "/api/v1/projects/acme/activity/export", http.StatusNotFound},
		"bad format":           {testPrincipal(uOwner1), "/api/v1/projects/acme/activity/export?format=xml", http.StatusBadRequest},
		"viewer may export":    {testPrincipal(uView1), "/api/v1/projects/acme/activity/export", http.StatusOK},
		"inverted range":       {testPrincipal(uOwner1), "/api/v1/projects/acme/activity/export?since=2026-02-01&until=2026-01-01", http.StatusBadRequest},
		"range of dates works": {testPrincipal(uOwner1), "/api/v1/projects/acme/activity/export?since=2026-01-01&until=2026-02-01", http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			db := newExportDB(projectExportLogs())
			rec, _ := serveExport(t, newExportProjectRouter(t, db), context.Background(), tc.p, tc.path)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}

			if tc.want != http.StatusOK && len(db.calls) != 0 {
				t.Errorf("audit log was read for a refused request")
			}
		})
	}
}

func TestExportFilenameIsSafe(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 30, 45, 0, time.FixedZone("x", 3600))

	for prefix, want := range map[string]string{
		"openartifact-audit":      "openartifact-audit-20260301T113045Z.csv",
		`a"b/../c d`:              "a-b-..-c-d-20260301T113045Z.csv",
		"":                        "audit-20260301T113045Z.csv",
		"\r\nX-Injected: 1":       "X-Injected-1-20260301T113045Z.csv",
		"openartifact-ü-activity": "openartifact---activity-20260301T113045Z.csv",
	} {
		if got := exportFilename(prefix, "csv", now); got != want {
			t.Errorf("exportFilename(%q) = %q, want %q", prefix, got, want)
		}
	}
}
