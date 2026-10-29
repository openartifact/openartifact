// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/osv"
	"github.com/openartifact/openartifact/internal/vuln"
)

// osvFixtureServer serves the OSV export of one ecosystem, like the public
// bucket: a zip with one JSON record per advisory, validated by an ETag.
type osvFixtureServer struct {
	mu      sync.Mutex
	records map[string]string
	etag    string
	hits    atomic.Int32
	full    atomic.Int32
	srv     *httptest.Server
}

func newOSVFixtureServer(t *testing.T) *osvFixtureServer {
	t.Helper()

	f := &osvFixtureServer{records: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))

	t.Cleanup(f.srv.Close)

	return f
}

// publish replaces the export and gives it a new validator.
func (f *osvFixtureServer) publish(etag string, records ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.records = map[string]string{}
	for i, r := range records {
		f.records[fmt.Sprintf("rec-%d.json", i)] = r
	}

	f.etag = etag
}

func (f *osvFixtureServer) serve(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)

	if r.URL.Path != "/npm/all.zip" {
		http.NotFound(w, r)

		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)

		return
	}

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for name, body := range f.records {
		fw, err := zw.Create(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		_, _ = fw.Write([]byte(body))
	}

	if err := zw.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	f.full.Add(1)
	w.Header().Set("ETag", f.etag)
	_, _ = w.Write(buf.Bytes())
}

func lodashAdvisory(id, modified, rangeEvents, withdrawn string) string {
	if withdrawn != "" {
		withdrawn = fmt.Sprintf(`"withdrawn": %q,`, withdrawn)
	}

	return fmt.Sprintf(`{"id": %q, "modified": %q, %s "aliases": ["CVE-%s"], "summary": "lodash flaw %s",
		"database_specific": {"severity": "HIGH"},
		"affected": [{"package": {"ecosystem": "npm", "name": "lodash"},
		  "ranges": [{"type": "SEMVER", "events": %s}]}]}`, id, modified, withdrawn, id, id, rangeEvents)
}

// publishedWebhook records the dispatched events.
type osvWebhookRecorder struct {
	mu     sync.Mutex
	events []map[string]any
}

func (r *osvWebhookRecorder) Dispatch(_ context.Context, repoID, eventType string, payload map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cp := map[string]any{"repo_id": repoID, "event": eventType}
	maps.Copy(cp, payload)

	r.events = append(r.events, cp)
}

// take returns and clears the recorded events.
func (r *osvWebhookRecorder) take() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := r.events
	r.events = nil

	return out
}

type osvSeed struct {
	*cleanupSeed

	hostedRepo string
	proxyRepo2 string
	pkgID      string
}

// addNPMVersion stores a hosted npm version.
func (s *osvSeed) addNPMVersion(version string) {
	s.t.Helper()

	s.seq++

	blobID, _ := s.blob(fmt.Sprintf("npm tarball %d", s.seq), 2*time.Hour)

	if _, err := s.pg.GetDB().ExecContext(context.Background(),
		`WITH v AS (INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata)
		            VALUES ($1, $4, $2::text, $2::text, '{"manifest_json": {}}') RETURNING id)
		 INSERT INTO package_files (version_id, repository_id, path, filename, blob_id)
		 SELECT v.id, $4, 'lodash/-/lodash-' || $2::text || '.tgz', 'lodash-' || $2::text || '.tgz', $3 FROM v`,
		s.pkgID, version, blobID, s.hostedRepo); err != nil {
		s.t.Fatal(err)
	}
}

func newOSVSeed(t *testing.T) *osvSeed {
	t.Helper()

	s := &osvSeed{cleanupSeed: newCleanupSeed(t)}
	ctx := context.Background()
	org := trashTestOrgID

	repo := &models.Repository{Name: "osv-hosted-npm", Format: "npm", RepoType: "hosted", ProjectID: &org}
	if err := s.pg.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	s.hostedRepo = repo.ID
	s.proxyRepo2 = s.proxyRepo("osv-proxy-npm", 3600)

	if err := s.pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO packages (repository_id, format, name, name_key, attrs) VALUES ($1, 'npm', 'lodash', 'lodash', '{"scope": "", "name": "lodash"}') RETURNING id`, s.hostedRepo).
		Scan(&s.pkgID); err != nil {
		t.Fatal(err)
	}

	return s
}

// findings returns the stored osv findings by "repo-kind name version", where
// a package with no result has no entry.
func (s *osvSeed) findings(t *testing.T) map[string][]string {
	t.Helper()

	rows, err := s.pg.GetDB().QueryContext(context.Background(), `
		SELECT r.name, vr.package_version, vr.vulnerabilities::text, vr.vuln_count
		FROM vuln_results vr
		JOIN scan_jobs sj ON sj.id = vr.scan_job_id AND sj.scanner = 'osv'
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		JOIN repositories r ON r.id = ref.repository_id`)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	out := map[string][]string{}

	for rows.Next() {
		var name, version, raw string

		var count int
		if err := rows.Scan(&name, &version, &raw, &count); err != nil {
			t.Fatal(err)
		}

		key := name + " " + version
		out[key] = []string{}

		for _, id := range []string{"GHSA-0001", "GHSA-0002"} {
			if strings.Contains(raw, id) {
				out[key] = append(out[key], id)
			}
		}

		if len(out[key]) != count {
			t.Fatalf("%s: vuln_count %d does not match findings %s", key, count, raw)
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

// expectFindings fails unless the stored osv findings are exactly want, by
// "repository version" (a closed finding stays as an empty list).
func (s *osvSeed) expectFindings(t *testing.T, label string, want map[string][]string) {
	t.Helper()

	got := s.findings(t)

	if len(got) != len(want) {
		t.Fatalf("%s: findings = %v, want %v", label, got, want)
	}

	for key, ids := range want {
		if !slices.Equal(got[key], ids) {
			t.Fatalf("%s: findings of %s = %v, want %v (all: %v)", label, key, got[key], ids, got)
		}
	}
}

// expectWebhooks fails unless n vulnerability.new events were raised, each
// listing exactly one advisory, and returns nothing: the recorder is drained.
func expectWebhooks(t *testing.T, hooks *osvWebhookRecorder, label string, n int, listed int) {
	t.Helper()

	events := hooks.take()
	if len(events) != n {
		t.Fatalf("%s: %d webhooks, want %d: %v", label, len(events), n, events)
	}

	for _, ev := range events {
		vulns, _ := ev["vulnerabilities"].([]any)
		if ev["event"] != "vulnerability.new" || ev["source"] != "osv-mirror" || len(vulns) != listed {
			t.Fatalf("%s: unexpected event (want %d advisories listed): %v", label, listed, ev)
		}
	}
}

// TestOSVMirrorImportRematchLifecycle drives the mirror through successive
// imports of a fixture export: a silent baseline, a new advisory that creates
// findings on already stored versions (hosted and proxy cache) and raises the
// webhook, a withdrawal that closes a finding, an unchanged export (304) and a
// version uploaded after the last import.
func TestOSVMirrorImportRematchLifecycle(t *testing.T) {
	s := newOSVSeed(t)
	ctx := context.Background()

	s.addNPMVersion("4.17.20")
	s.addNPMVersion("4.17.21")

	_, sha := s.blob("cached tarball", 2*time.Hour)
	s.cacheEntry(s.proxyRepo2, "lodash/-/lodash-4.17.20.tgz", sha, -time.Hour)

	fixture := newOSVFixtureServer(t)

	importer, err := osv.NewImporter(s.pg, osv.Options{BaseURL: fixture.srv.URL, Ecosystems: []string{"npm"}, Client: fixture.srv.Client()},
		testLogger(t))
	if err != nil {
		t.Fatal(err)
	}

	w := NewOSVMirrorWorker(s.pg, importer, []string{"npm"}, testLogger(t), 0)
	hooks := &osvWebhookRecorder{}
	w.SetPublisher(hooks)

	const (
		fixedAt4_17_21 = `[{"introduced": "0"}, {"fixed": "4.17.21"}]`
		throughMinor   = `[{"introduced": "0"}, {"last_affected": "4.17.21"}]`
	)

	run := func(label string) OSVMirrorStats {
		t.Helper()

		stats, err := w.RunOnce(ctx)
		if err != nil {
			t.Fatalf("%s: RunOnce: %v", label, err)
		}

		return stats
	}

	// 1. Baseline: findings appear for stored versions, silently.
	fixture.publish("v1", lodashAdvisory("GHSA-0001", "2024-01-01T00:00:00Z", fixedAt4_17_21, ""))

	stats := run("baseline")
	if stats.Imported != 1 || !stats.Rematch["npm"].Silent {
		t.Fatalf("baseline stats = %+v", stats)
	}

	// The fixed version 4.17.21 has no finding, so no result at all.
	s.expectFindings(t, "baseline", map[string][]string{
		"osv-hosted-npm 4.17.20": {"GHSA-0001"},
		"osv-proxy-npm 4.17.20":  {"GHSA-0001"},
	})
	expectWebhooks(t, hooks, "baseline", 0, 0)

	// A policy quarantines the finding; the mirror must never lift it.
	s.exec(`UPDATE vuln_results SET quarantined = true WHERE package_version = '4.17.20'
		AND scan_job_id IN (SELECT sj.id FROM scan_jobs sj JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE ref.repository_id = $1)`, s.hostedRepo)

	// 2. A second advisory arrives: findings on stored versions, one event each.
	fixture.publish("v2",
		lodashAdvisory("GHSA-0001", "2024-01-01T00:00:00Z", fixedAt4_17_21, ""),
		lodashAdvisory("GHSA-0002", "2024-02-01T00:00:00Z", throughMinor, ""))

	stats = run("new advisory")
	if rs := stats.Rematch["npm"]; rs.Silent || rs.Detected != 3 {
		t.Fatalf("new advisory re-match = %+v; want 3 new findings (hosted 4.17.20 and 4.17.21, proxy 4.17.20)", rs)
	}

	s.expectFindings(t, "new advisory", map[string][]string{
		"osv-hosted-npm 4.17.20": {"GHSA-0001", "GHSA-0002"},
		"osv-hosted-npm 4.17.21": {"GHSA-0002"},
		"osv-proxy-npm 4.17.20":  {"GHSA-0001", "GHSA-0002"},
	})
	// One event per affected stored version, each listing only the new advisory.
	expectWebhooks(t, hooks, "new advisory", 3, 1)

	if n := s.count(`SELECT count(*) FROM vuln_results WHERE quarantined AND package_version = '4.17.20'
		AND scan_job_id IN (SELECT sj.id FROM scan_jobs sj JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE ref.repository_id = $1)`, s.hostedRepo); n != 1 {
		t.Fatalf("quarantined results of the hosted 4.17.20 = %d, want 1 (the mirror must keep the flag)", n)
	}

	// 3. The second advisory is withdrawn: its findings close.
	fixture.publish("v3",
		lodashAdvisory("GHSA-0001", "2024-01-01T00:00:00Z", fixedAt4_17_21, ""),
		lodashAdvisory("GHSA-0002", "2024-03-01T00:00:00Z", throughMinor, "2024-03-01T00:00:00Z"))

	stats = run("withdrawal")
	if rs := stats.Rematch["npm"]; rs.Resolved != 3 {
		t.Fatalf("withdrawal re-match = %+v; want 3 resolved findings", rs)
	}

	s.expectFindings(t, "withdrawal", map[string][]string{
		"osv-hosted-npm 4.17.20": {"GHSA-0001"},
		"osv-hosted-npm 4.17.21": {},
		"osv-proxy-npm 4.17.20":  {"GHSA-0001"},
	})
	expectWebhooks(t, hooks, "withdrawal", 0, 0)

	// 4. Unchanged export: a conditional request, nothing downloaded or changed.
	before := fixture.full.Load()

	stats = run("unchanged")
	if stats.Unchanged != 1 || fixture.full.Load() != before {
		t.Fatalf("unchanged stats = %+v, full downloads %d -> %d", stats, before, fixture.full.Load())
	}

	// 5. A version uploaded after the last import is matched on the next pass
	// although no advisory changed, and raises its event: only the new version is news.
	s.addNPMVersion("4.17.19")

	run("new upload")
	s.expectFindings(t, "new upload", map[string][]string{
		"osv-hosted-npm 4.17.19": {"GHSA-0001"},
		"osv-hosted-npm 4.17.20": {"GHSA-0001"},
		"osv-hosted-npm 4.17.21": {},
		"osv-proxy-npm 4.17.20":  {"GHSA-0001"},
	})
	expectWebhooks(t, hooks, "new upload", 1, 1)

	st, err := s.pg.GetOSVMirrorState(ctx, "npm")
	if err != nil || st == nil || st.Advisories != 1 || st.RematchPending || st.LastError != "" || st.HighWater == nil {
		t.Fatalf("mirror state = %+v, %v; want 1 advisory, nothing pending, no error", st, err)
	}
}

// A failing source is recorded in the state, leaves the high-water mark alone
// and keeps the scanner on the online fallback.
func TestOSVMirrorImportFailureIsRecorded(t *testing.T) {
	s := newOSVSeed(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	importer, err := osv.NewImporter(s.pg, osv.Options{BaseURL: srv.URL, Ecosystems: []string{"npm"}, Client: srv.Client()}, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}

	w := NewOSVMirrorWorker(s.pg, importer, []string{"npm"}, testLogger(t), 0)

	stats, err := w.RunOnce(ctx)
	if err != nil || stats.Failed != 1 {
		t.Fatalf("RunOnce = %+v, %v; want one failed ecosystem and no pass error", stats, err)
	}

	st, err := s.pg.GetOSVMirrorState(ctx, "npm")
	if err != nil || st == nil || st.LastError == "" || st.HighWater != nil {
		t.Fatalf("state = %+v, %v; want the error recorded and no high-water mark", st, err)
	}

	if ready, err := vuln.NewMirror(s.pg).Ready(ctx, "npm"); err != nil || ready {
		t.Fatalf("Ready = %v, %v; an empty mirror must not be used", ready, err)
	}
}

// Another replica holding the lock makes the pass a no-op.
func TestOSVMirrorSkipsWhileAnotherReplicaHoldsTheLock(t *testing.T) {
	s := newOSVSeed(t)
	ctx := context.Background()

	fixture := newOSVFixtureServer(t)
	fixture.publish("v1", lodashAdvisory("GHSA-0001", "2024-01-01T00:00:00Z", `[{"introduced": "0"}]`, ""))

	importer, err := osv.NewImporter(s.pg, osv.Options{BaseURL: fixture.srv.URL, Ecosystems: []string{"npm"}, Client: fixture.srv.Client()},
		testLogger(t))
	if err != nil {
		t.Fatal(err)
	}

	other, err := s.pg.GetDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = other.Close() }()

	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, osvMirrorLockKey); err != nil {
		t.Fatal(err)
	}

	w := NewOSVMirrorWorker(s.pg, importer, []string{"npm"}, testLogger(t), 0)

	stats, err := w.RunOnce(ctx)
	if err != nil || !stats.Busy || fixture.hits.Load() != 0 {
		t.Fatalf("RunOnce under a held lock = %+v, %v, %d requests; want Busy and no download", stats, err, fixture.hits.Load())
	}
}
