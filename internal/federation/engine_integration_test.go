// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	engInfoPath = "/federation/v1/info"
	engLogPath  = "/federation/v1/log"
	engUnitPath = "/federation/v1/unit"
	engBlobPath = "/federation/v1/blob"
)

// engIntercept lets a test stand between the engine and a site's peer
// endpoints: it answers the request itself or hands it to next.
type engIntercept func(w http.ResponseWriter, r *http.Request, next http.Handler)

type engRequest struct {
	Path  string
	Query url.Values
}

type engNotifier struct {
	mu  sync.Mutex
	npm []string
}

func (n *engNotifier) NotifyMaven(_, _, _ string) {}

func (n *engNotifier) NotifyNPM(packageID string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.npm = append(n.npm, packageID)
}

func (n *engNotifier) npmPackages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	return slices.Clone(n.npm)
}

type engPublisher struct {
	mu     sync.Mutex
	events []string
}

func (p *engPublisher) Dispatch(_ context.Context, repoID, eventType string, _ map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.events = append(p.events, eventType+"@"+repoID)
}

func (p *engPublisher) count(eventType, repoID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(slices.DeleteFunc(slices.Clone(p.events), func(e string) bool { return e != eventType+"@"+repoID }))
}

// engSite is one OpenArtifact instance of the tests: its own database, blob
// store, peer endpoints behind an httptest server and an Engine pulling from
// the other sites.
type engSite struct {
	name   string
	pg     *database.PostgresDB
	db     *sql.DB
	store  *Store
	blobs  *storage.FilesystemStore
	repoID string
	token  string
	opts   Options
	eng    *Engine
	srv    *httptest.Server
	api    http.Handler
	notes  *engNotifier
	events *engPublisher

	mu        sync.Mutex
	intercept engIntercept
	reqs      []engRequest
}

func engLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	return log
}

func engCtx(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	return ctx
}

// newEngSite creates a site with a federated repository named name of format.
func newEngSite(t *testing.T, name, format string) *engSite {
	t.Helper()

	t.Setenv(netutil.EnvAllowPrivate, "true")

	ctx := engCtx(t)
	pg := freshDB(t)

	blobs, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}

	s := &engSite{
		name: name, pg: pg, db: pg.GetDB(), store: NewStore(pg.GetDB()), blobs: blobs,
		notes: &engNotifier{}, events: &engPublisher{},
	}

	s.repoID = seedRepo(t, s.db, name, format)

	if err := s.store.Enable(ctx, s.repoID, format); err != nil {
		t.Fatalf("enable federation: %v", err)
	}

	if _, s.token, err = s.store.CreateToken(ctx, s.repoID, "peer", "test"); err != nil {
		t.Fatalf("create token: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/federation/v1/", http.StripPrefix("/federation/v1", NewServer(s.store, pg, blobs, engLogger(t)).Handler()))
	s.api = mux
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))

	t.Cleanup(s.srv.Close)

	metrics, err := NewMetrics(prometheus.NewRegistry(), s.store)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}

	s.opts = Options{
		Store: s.store, SQL: s.db, DB: pg, Blobs: blobs, Log: engLogger(t), Webhooks: s.events, Notify: s.notes, Metrics: metrics,
		PollInterval: time.Second, BatchSize: 100, MaxBackoff: time.Minute, RequestTimeout: 30 * time.Second,
		TombstoneRetention: 30 * 24 * time.Hour, TrashDefaultDays: 7,
	}
	s.newEngine(t)

	return s
}

// newEngine (re)creates the site's engine, which reads the instance id once.
func (s *engSite) newEngine(t *testing.T) {
	t.Helper()

	eng, err := NewEngine(engCtx(t), s.opts)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	s.eng = eng
}

func (s *engSite) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, engRequest{Path: r.URL.Path, Query: r.URL.Query()})
	hook := s.intercept
	s.mu.Unlock()

	if hook != nil {
		hook(w, r, s.api)

		return
	}

	s.api.ServeHTTP(w, r)
}

func (s *engSite) setIntercept(h engIntercept) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.intercept = h
}

func (s *engSite) resetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reqs = nil
}

// count returns how many requests for path (matching, when not nil) the site served.
func (s *engSite) count(path string, match func(url.Values) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0

	for _, r := range s.reqs {
		if r.Path == path && (match == nil || match(r.Query)) {
			n++
		}
	}

	return n
}

func engFromZero(q url.Values) bool { return q.Get("after") == "0" }

func (s *engSite) exec(t *testing.T, query string, args ...any) {
	t.Helper()

	if _, err := s.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: exec %q: %v", s.name, query, err)
	}
}

func (s *engSite) scalar(t *testing.T, query string, args ...any) int64 {
	t.Helper()

	var n int64

	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: query %q: %v", s.name, query, err)
	}

	return n
}

func engSHA(content string) string {
	sum := sha256.Sum256([]byte(content))

	return hex.EncodeToString(sum[:])
}

// put writes (or replaces) a raw artifact through plain row writes, so the
// capture triggers log it, with its bytes in the site's blob store.
func (s *engSite) put(t *testing.T, path, content string) {
	t.Helper()
	s.putIn(t, s.repoID, path, content)
}

func (s *engSite) putIn(t *testing.T, repoID, path, content string) {
	t.Helper()

	ctx := context.Background()

	if _, err := s.blobs.Put(ctx, strings.NewReader(content), map[string]string{"sha256": engSHA(content)}); err != nil {
		t.Fatalf("%s: store blob: %v", s.name, err)
	}

	blobID, _ := seedBlob(t, s.db, content)

	res, err := s.db.ExecContext(ctx,
		`UPDATE raw_artifacts SET blob_id = $3 WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL`, repoID, path, blobID)
	if err != nil {
		t.Fatalf("%s: update artifact: %v", s.name, err)
	}

	if n, _ := res.RowsAffected(); n > 0 {
		return
	}

	s.exec(t, `INSERT INTO raw_artifacts (repository_id, path, blob_id) VALUES ($1, $2, $3)`, repoID, path, blobID)
}

func (s *engSite) trash(t *testing.T, path string) {
	t.Helper()

	s.exec(t, `UPDATE raw_artifacts SET deleted_at = now(), deleted_by = 'test' WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL`, s.repoID, path)
}

func (s *engSite) purge(t *testing.T, path string) {
	t.Helper()

	s.exec(t, `DELETE FROM raw_artifacts WHERE repository_id = $1 AND path = $2 AND deleted_at IS NULL`, s.repoID, path)
}

// live returns path -> sha256 of the live raw artifacts of the site's repository.
func (s *engSite) live(t *testing.T) map[string]string {
	t.Helper()

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT r.path, b.sha256 FROM raw_artifacts r JOIN blobs b ON b.id = r.blob_id
		  WHERE r.repository_id = $1 AND r.deleted_at IS NULL`, s.repoID)
	if err != nil {
		t.Fatalf("%s: list artifacts: %v", s.name, err)
	}
	defer rows.Close()

	out := map[string]string{}

	for rows.Next() {
		var p, sha string
		if err := rows.Scan(&p, &sha); err != nil {
			t.Fatalf("%s: scan artifact: %v", s.name, err)
		}

		out[p] = sha
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("%s: list artifacts: %v", s.name, err)
	}

	return out
}

// read returns the bytes of a live artifact, read back through the blob store.
func (s *engSite) read(t *testing.T, path string) (string, bool) {
	t.Helper()

	sha, ok := s.live(t)[path]
	if !ok {
		return "", false
	}

	body, err := s.blobs.Get(context.Background(), sha)
	if err != nil {
		t.Fatalf("%s: open blob of %s: %v", s.name, path, err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("%s: read blob of %s: %v", s.name, path, err)
	}

	if got := engSHA(string(data)); got != sha {
		t.Fatalf("%s: stored bytes of %s hash to %s, row says %s", s.name, path, got, sha)
	}

	return string(data), true
}

func (s *engSite) mustRead(t *testing.T, path, want string) {
	t.Helper()

	got, ok := s.read(t, path)
	if !ok || got != want {
		t.Errorf("%s: %s = %q (live %v), want %q", s.name, path, got, ok, want)
	}
}

func (s *engSite) mustBeAbsent(t *testing.T, path string) {
	t.Helper()

	if got, ok := s.read(t, path); ok {
		t.Errorf("%s: %s is live with %q, want absent", s.name, path, got)
	}
}

// hasBlob reports whether the site holds the blob in its store and in its blobs table.
func (s *engSite) hasBlob(t *testing.T, sha string) (bool, bool) {
	t.Helper()

	inStore, err := s.blobs.Exists(context.Background(), sha)
	if err != nil {
		t.Fatalf("%s: blob exists: %v", s.name, err)
	}

	return inStore, s.scalar(t, `SELECT count(*) FROM blobs WHERE sha256 = $1`, sha) > 0
}

func (s *engSite) mustHaveNoBlob(t *testing.T, content string) {
	t.Helper()

	if inStore, inTable := s.hasBlob(t, engSHA(content)); inStore || inTable {
		t.Errorf("%s: blob of %q present (store %v, table %v), want none", s.name, content, inStore, inTable)
	}
}

func (s *engSite) entry(t *testing.T, key string) *Entry {
	t.Helper()

	e, err := s.store.GetEntry(context.Background(), s.db, s.repoID, key)
	if err != nil {
		t.Fatalf("%s: log entry %s: %v", s.name, key, err)
	}

	return e
}

func (s *engSite) hasEntry(t *testing.T, key string) bool {
	t.Helper()

	_, err := s.store.GetEntry(context.Background(), s.db, s.repoID, key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("%s: log entry %s: %v", s.name, key, err)
	}

	return err == nil
}

func (s *engSite) setStamp(t *testing.T, key string, st Stamp) {
	t.Helper()

	s.exec(t, `UPDATE federation_units SET hlc_wall = $3, hlc_counter = $4, origin = $5 WHERE repo_id = $1 AND unit_key = $2`,
		s.repoID, key, st.Wall, st.Counter, st.Origin)
}

func (s *engSite) instance(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := s.store.InstanceID(context.Background())
	if err != nil {
		t.Fatalf("%s: instance id: %v", s.name, err)
	}

	return id
}

func (s *engSite) state(t *testing.T) *RepoState {
	t.Helper()

	st, err := s.store.RepoState(context.Background(), s.repoID)
	if err != nil {
		t.Fatalf("%s: repo state: %v", s.name, err)
	}

	return st
}

// logView renders the site's log as unit -> "event|stamp|tombstone|ref", with
// the sequence number when withSeq is set.
func (s *engSite) logView(t *testing.T, withSeq bool) map[string]string {
	t.Helper()

	entries, err := s.store.ListEntries(context.Background(), s.repoID, 0, 10000)
	if err != nil {
		t.Fatalf("%s: list log: %v", s.name, err)
	}

	out := map[string]string{}

	for _, e := range entries {
		v := fmt.Sprintf("%s|%s|%v|%s", e.Event, e.Stamp, e.Tombstone, e.ContentRef)
		if withSeq {
			v = fmt.Sprintf("%d|%s", e.Seq, v)
		}

		out[e.UnitKey] = v
	}

	return out
}

func (s *engSite) addPeer(t *testing.T, from *engSite) *Peer {
	t.Helper()

	p, err := s.store.CreatePeer(context.Background(), NewPeer{RepoID: s.repoID, Name: from.name, BaseURL: from.srv.URL, Token: from.token})
	if err != nil {
		t.Fatalf("%s: create peer %s: %v", s.name, from.name, err)
	}

	return p
}

func (s *engSite) pull(t *testing.T, p *Peer) error {
	t.Helper()

	return s.eng.PullPeer(engCtx(t), p.ID, s.repoID)
}

func (s *engSite) mustPull(t *testing.T, p *Peer) {
	t.Helper()

	if err := s.pull(t, p); err != nil {
		t.Fatalf("%s pulling %s: %v", s.name, p.Name, err)
	}
}

func (s *engSite) peer(t *testing.T, p *Peer) *Peer {
	t.Helper()

	got, err := s.store.GetPeer(context.Background(), s.repoID, p.ID)
	if err != nil {
		t.Fatalf("%s: reload peer %s: %v", s.name, p.Name, err)
	}

	return got
}

func (s *engSite) deferred(t *testing.T, p *Peer) []Deferred {
	t.Helper()

	d, err := s.store.ListDeferred(context.Background(), p.ID, false, 100)
	if err != nil {
		t.Fatalf("%s: list deferred: %v", s.name, err)
	}

	return d
}

// makeDue lets the peer's deferred units be retried by the next pull.
func (s *engSite) makeDue(t *testing.T, p *Peer) {
	t.Helper()

	s.exec(t, `UPDATE federation_deferred SET next_attempt_at = now() WHERE peer_id = $1::uuid`, p.ID)
}

// addRepo adds another federated raw repository to the site and a peer token for it.
func (s *engSite) addRepo(t *testing.T, name string) (string, string) {
	t.Helper()

	repoID := seedRepo(t, s.db, name, "raw")

	if err := s.store.Enable(context.Background(), repoID, "raw"); err != nil {
		t.Fatalf("%s: enable %s: %v", s.name, name, err)
	}

	_, token, err := s.store.CreateToken(context.Background(), repoID, "peer", "test")
	if err != nil {
		t.Fatalf("%s: token for %s: %v", s.name, name, err)
	}

	return repoID, token
}

// quota returns the usage counters of the site's repository.
func (s *engSite) quota(t *testing.T) (int64, int64) {
	t.Helper()

	var size, count int64

	err := s.db.QueryRowContext(context.Background(),
		`SELECT current_size_bytes, current_artifact_count FROM storage_quotas WHERE repository_id = $1`, s.repoID).Scan(&size, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0
	}

	if err != nil {
		t.Fatalf("%s: read quota: %v", s.name, err)
	}

	return size, count
}

func (s *engSite) wantQuota(t *testing.T, size, count int64) {
	t.Helper()

	gotSize, gotCount := s.quota(t)
	if gotSize != size || gotCount != count {
		t.Errorf("%s: quota usage = %d bytes, %d artifacts; want %d, %d", s.name, gotSize, gotCount, size, count)
	}

	if n := s.scalar(t, `SELECT count(*) FROM quota_pending WHERE repository_id = $1`, s.repoID); n != 0 {
		t.Errorf("%s: %d quota_pending entries left behind", s.name, n)
	}
}

func engEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func engEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// engGet performs a raw request against a peer endpoint with the given
// Authorization header ("" sends none).
func engGet(t *testing.T, base, pathAndQuery, authorization string) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(engCtx(t), http.MethodGet, base+pathAndQuery, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", pathAndQuery, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", pathAndQuery, err)
	}

	return resp.StatusCode, resp.Header, body
}

func engBearer(token string) string { return "Bearer " + token }

func engErrCode(t *testing.T, body []byte) string {
	t.Helper()

	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body %q is not JSON: %v", body, err)
	}

	return e.Code
}

// engPassThrough writes a recorded answer to the real response.
func engPassThrough(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	maps.Copy(w.Header(), rec.Header())

	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// engCorruptBlob answers the blob requests of unit with mutate(real bytes).
func engCorruptBlob(unit string, mutate func([]byte) []byte) engIntercept {
	return func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.URL.Path != engBlobPath || r.URL.Query().Get("unit") != unit {
			next.ServeHTTP(w, r)

			return
		}

		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(rec.Code)
		_, _ = w.Write(mutate(rec.Body.Bytes()))
	}
}

// engFailBlob answers the blob requests of unit with a 500.
func engFailBlob(unit string) engIntercept {
	return func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.URL.Path == engBlobPath && r.URL.Query().Get("unit") == unit {
			http.Error(w, "storage offline", http.StatusInternalServerError)

			return
		}

		next.ServeHTTP(w, r)
	}
}

// engRewrite edits the JSON answer of path before the engine sees it.
func engRewrite(path string, edit func(r *http.Request, doc map[string]any)) engIntercept {
	return func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.URL.Path != path {
			next.ServeHTTP(w, r)

			return
		}

		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
		dec.UseNumber()

		var doc map[string]any
		if rec.Code != http.StatusOK || dec.Decode(&doc) != nil {
			engPassThrough(w, rec)

			return
		}

		edit(r, doc)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.Code)
		_ = json.NewEncoder(w).Encode(doc)
	}
}

// ---------------------------------------------------------------------------
// 1. Basic pull

func TestEngineBasicPullReplicatesUnitsAndBytes(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	files := map[string]string{
		"docs/readme.txt": "hello federation",
		"bin/tool":        strings.Repeat("0123456789abcdef", 8192) + "tail",
		"a/b/c.dat":       "z",
	}
	for p, c := range files {
		a.put(t, p, c)
	}

	headA := a.state(t).HeadSeq
	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	got := b.peer(t, peer)
	engEqual(t, "applied_total", got.AppliedTotal, int64(len(files)))
	engEqual(t, "cursor", got.CursorSeq, headA)
	engEqual(t, "remote head", got.RemoteHeadSeq, headA)
	engEqual(t, "error count", got.ErrorCount, 0)

	if got.LastSuccessAt == nil || got.LastError != "" || got.State() != PeerOK {
		t.Errorf("peer state = %s (success %v, error %q), want ok", got.State(), got.LastSuccessAt, got.LastError)
	}

	for p, c := range files {
		b.mustRead(t, p, c)

		ea, eb := a.entry(t, p), b.entry(t, p)
		if eb.Stamp != ea.Stamp || eb.Event != ea.Event || eb.Tombstone || eb.ContentRef != engSHA(c) {
			t.Errorf("log entry of %s on b = %+v, want the stamp, event and content of a's %+v", p, eb, ea)
		}

		engEqual(t, "origin of "+p, eb.Stamp.Origin, a.instance(t))
	}

	// Recorded once: one fresh local sequence number per unit, no more.
	engEqual(t, "b head_seq", b.state(t).HeadSeq, int64(len(files)))
	engEqual(t, "b log vs a log", b.logView(t, false), a.logView(t, false))
	engEqual(t, "units fetched", a.count(engUnitPath, nil), len(files))
	engEqual(t, "blobs fetched", a.count(engBlobPath, nil), len(files))
	engEqual(t, "uploaded webhooks", b.events.count(webhook.EventArtifactUploaded, b.repoID), len(files))

	// A second pull has nothing to do.
	b.mustPull(t, peer)
	engEqual(t, "applied_total after a repeat", b.peer(t, peer).AppliedTotal, int64(len(files)))
	engEqual(t, "b head_seq after a repeat", b.state(t).HeadSeq, int64(len(files)))
	engEqual(t, "units fetched after a repeat", a.count(engUnitPath, nil), len(files))
}

func TestEngineNoEchoWhenPullingBack(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "one", "first")
	a.put(t, "two", "second")
	b.mustPull(t, b.addPeer(t, a))

	logBefore, liveBefore, headBefore := a.logView(t, true), a.live(t), a.state(t).HeadSeq

	back := a.addPeer(t, b)
	a.mustPull(t, back)

	got := a.peer(t, back)
	engEqual(t, "applied_total", got.AppliedTotal, int64(0))
	engEqual(t, "cursor", got.CursorSeq, b.state(t).HeadSeq)
	engEqual(t, "a log", a.logView(t, true), logBefore)
	engEqual(t, "a live rows", a.live(t), liveBefore)
	engEqual(t, "a head_seq", a.state(t).HeadSeq, headBefore)
	engEqual(t, "units fetched from b", b.count(engUnitPath, nil), 0)
	engEqual(t, "blobs fetched from b", b.count(engBlobPath, nil), 0)
	engEqual(t, "uploaded webhooks on a", a.events.count(webhook.EventArtifactUploaded, a.repoID), 0)
}

// ---------------------------------------------------------------------------
// 2. Last writer wins

func TestEngineOlderStampNeverOverwritesNewerLocalState(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "f", "old")
	b.put(t, "f", "new-local")
	a.setStamp(t, "f", Stamp{Wall: 1000, Origin: a.instance(t)})
	b.setStamp(t, "f", Stamp{Wall: 2000, Origin: b.instance(t)})

	pab := b.addPeer(t, a)
	b.mustPull(t, pab)

	got := b.peer(t, pab)
	engEqual(t, "applied_total", got.AppliedTotal, int64(0))
	engEqual(t, "cursor", got.CursorSeq, a.state(t).HeadSeq)
	b.mustRead(t, "f", "new-local")
	engEqual(t, "b stamp", b.entry(t, "f").Stamp, Stamp{Wall: 2000, Origin: b.instance(t)})
	engEqual(t, "units fetched from a", a.count(engUnitPath, nil), 0)
	b.mustHaveNoBlob(t, "old")

	// The newer state travels the other way and replaces the older one.
	pba := a.addPeer(t, b)
	a.mustPull(t, pba)

	gotA := a.peer(t, pba)
	engEqual(t, "a applied_total", gotA.AppliedTotal, int64(1))
	engEqual(t, "a conflicts_total", gotA.ConflictsTotal, int64(1))
	a.mustRead(t, "f", "new-local")
	engEqual(t, "a stamp", a.entry(t, "f").Stamp, Stamp{Wall: 2000, Origin: b.instance(t)})

	// Converged: pulling again changes nothing on either side.
	b.mustPull(t, pab)
	engEqual(t, "b applied_total", b.peer(t, pab).AppliedTotal, int64(0))
	engEqual(t, "logs", a.logView(t, false), b.logView(t, false))
}

func TestEngineRecordUnitOrdersStamps(t *testing.T) {
	var (
		low  = uuid.MustParse("7fffffff-ffff-ffff-ffff-ffffffffffff")
		mid  = uuid.MustParse("80000000-0000-0000-0000-000000000000")
		high = uuid.MustParse("80000000-0000-0000-0000-000000000001")
	)

	local := Stamp{Wall: 100, Counter: 5, Origin: mid}

	tests := []struct {
		name      string
		incoming  Stamp
		tombstone bool
		wins      bool
	}{
		{"older wall", Stamp{99, 9, high}, false, false},
		{"same wall, older counter", Stamp{100, 4, high}, false, false},
		{"identical stamp", local, false, false},
		{"same wall and counter, lower origin", Stamp{100, 5, low}, false, false},
		{"same wall and counter, higher origin", Stamp{100, 5, high}, false, true},
		{"same wall, newer counter", Stamp{100, 6, low}, false, true},
		{"newer wall", Stamp{101, 0, low}, false, true},
		{"newer wall tombstone", Stamp{101, 0, low}, true, true},
	}

	s := newEngSite(t, "site-a", "raw")
	s.put(t, "f", "v")

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s.exec(t, `UPDATE federation_units SET tombstone = false, event = 'artifact_added' WHERE repo_id = $1 AND unit_key = 'f'`, s.repoID)
			s.setStamp(t, "f", local)

			before, headBefore := s.entry(t, "f"), s.state(t).HeadSeq

			tx, err := s.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}

			defer func() { _ = tx.Rollback() }()

			entry := Entry{UnitKey: "f", Kind: KindArtifact, Event: EventArtifactAdded, ContentRef: before.ContentRef, Stamp: tc.incoming}
			if tc.tombstone {
				entry.Event = EventArtifactDeleted
			}

			err = s.eng.recordUnit(context.Background(), tx, s.repoID, entry, tc.tombstone)
			if !tc.wins {
				if !errors.Is(err, ErrStale) {
					t.Fatalf("recordUnit = %v, want ErrStale", err)
				}

				_ = tx.Rollback()

				engEqual(t, "entry", s.entry(t, "f").Stamp, local)
				engEqual(t, "head_seq", s.state(t).HeadSeq, headBefore)

				return
			}

			if err != nil {
				t.Fatalf("recordUnit: %v", err)
			}

			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}

			after := s.entry(t, "f")
			if after.Stamp != tc.incoming || after.Tombstone != tc.tombstone || after.Seq <= before.Seq {
				t.Errorf("entry after = %+v, want stamp %s tombstone %v and a fresh seq", after, tc.incoming, tc.tombstone)
			}
		})
	}
}

func TestEngineEqualStampsAreBrokenByOrigin(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "f", "from-a")
	b.put(t, "f", "from-b")

	instA, instB := a.instance(t), b.instance(t)
	a.setStamp(t, "f", Stamp{Wall: 5000, Origin: instA})
	b.setStamp(t, "f", Stamp{Wall: 5000, Origin: instB})

	winner, winnerInst := "from-b", instB
	if bytes.Compare(instA[:], instB[:]) > 0 {
		winner, winnerInst = "from-a", instA
	}

	pab, pba := b.addPeer(t, a), a.addPeer(t, b)
	b.mustPull(t, pab)
	a.mustPull(t, pba)
	b.mustPull(t, pab)

	a.mustRead(t, "f", winner)
	b.mustRead(t, "f", winner)
	engEqual(t, "a stamp origin", a.entry(t, "f").Stamp.Origin, winnerInst)
	engEqual(t, "b stamp origin", b.entry(t, "f").Stamp.Origin, winnerInst)
	engEqual(t, "logs", a.logView(t, false), b.logView(t, false))
}

// ---------------------------------------------------------------------------
// 3. Tombstones

func TestEngineTombstoneOrderingConverges(t *testing.T) {
	tests := []struct {
		name  string
		order []string // which peer b pulls first
	}{
		{"create reaches b before the delete", []string{"c", "a"}},
		{"delete reaches b before the create", []string{"a", "c"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw"), newEngSite(t, "site-c", "raw")
			peers := map[string]*Peer{"a": b.addPeer(t, a), "c": b.addPeer(t, c)}

			// c creates f first (stamp 1000); a then creates and deletes it (stamp 2000).
			c.put(t, "f", "from-c")
			c.setStamp(t, "f", Stamp{Wall: 1000, Origin: c.instance(t)})
			a.put(t, "f", "from-a")
			a.trash(t, "f")
			a.setStamp(t, "f", Stamp{Wall: 2000, Origin: a.instance(t)})

			for _, name := range tc.order {
				b.mustPull(t, peers[name])
			}

			b.mustBeAbsent(t, "f")

			if e := b.entry(t, "f"); !e.Tombstone || e.Stamp.Wall != 2000 || e.Stamp.Origin != a.instance(t) {
				t.Errorf("b entry = %+v, want a's tombstone at 2000", e)
			}

			if tc.order[0] == "a" {
				// The older create is never fetched once the tombstone is known.
				engEqual(t, "blobs fetched from c", c.count(engBlobPath, nil), 0)
				b.mustHaveNoBlob(t, "from-c")
			}

			// c writes f again, later than the delete: the resurrection wins everywhere.
			c.put(t, "f", "from-c-v2")
			c.setStamp(t, "f", Stamp{Wall: 3000, Origin: c.instance(t)})
			b.mustPull(t, peers["c"])
			b.mustRead(t, "f", "from-c-v2")

			before := b.peer(t, peers["a"]).AppliedTotal
			b.mustPull(t, peers["a"])
			engEqual(t, "applied from a's older tombstone", b.peer(t, peers["a"]).AppliedTotal, before)
			b.mustRead(t, "f", "from-c-v2")

			a.mustPull(t, a.addPeer(t, b))
			a.mustRead(t, "f", "from-c-v2")

			engEqual(t, "a vs b log", a.logView(t, false), b.logView(t, false))
			engEqual(t, "c vs b log", c.logView(t, false), b.logView(t, false))
		})
	}
}

func TestEngineDeleteIsAppliedLikeALocalDelete(t *testing.T) {
	tests := []struct {
		name      string
		retention int
		purge     bool
	}{
		{"trashed at the peer, trash kept", 7, false},
		{"purged at the peer, trash kept", 7, true},
		{"trashed at the peer, no trash", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
			b.exec(t, `UPDATE repositories SET trash_retention_days = $2 WHERE id = $1`, b.repoID, tc.retention)

			a.put(t, "f", "payload")

			peer := b.addPeer(t, a)
			b.mustPull(t, peer)
			b.mustRead(t, "f", "payload")

			if tc.purge {
				a.purge(t, "f")
			} else {
				a.trash(t, "f")
			}

			b.mustPull(t, peer)

			engEqual(t, "applied_total", b.peer(t, peer).AppliedTotal, int64(2))
			b.mustBeAbsent(t, "f")

			ea, eb := a.entry(t, "f"), b.entry(t, "f")
			if !eb.Tombstone || eb.Stamp != ea.Stamp || eb.Event != EventArtifactDeleted {
				t.Errorf("b entry = %+v, want a's tombstone %+v", eb, ea)
			}

			trashed := b.scalar(t, `SELECT count(*) FROM raw_artifacts WHERE repository_id = $1 AND path = 'f' AND deleted_by = 'federation:site-a'`, b.repoID)
			_, inTable := b.hasBlob(t, engSHA("payload"))

			if tc.retention > 0 {
				engEqual(t, "trashed rows", trashed, int64(1))
				engEqual(t, "blob kept for the trash", inTable, true)
			} else {
				engEqual(t, "trashed rows", trashed, int64(0))
				engEqual(t, "rows of f", b.scalar(t, `SELECT count(*) FROM raw_artifacts WHERE repository_id = $1`, b.repoID), int64(0))
				engEqual(t, "blob released", inTable, false)
			}

			engEqual(t, "deleted webhooks", b.events.count(webhook.EventArtifactDeleted, b.repoID), 1)
		})
	}
}

// ---------------------------------------------------------------------------
// 4. Integrity

func TestEngineCorruptBlobIsRejectedAndParked(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"other bytes of the same length", func(b []byte) []byte { return bytes.Repeat([]byte{'X'}, len(b)) }},
		{"truncated", func(b []byte) []byte { return b[:len(b)/2] }},
		{"extended", func(b []byte) []byte { return append(slices.Clone(b), '!') }},
		{"empty", func([]byte) []byte { return nil }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

			a.put(t, "good", "fine")
			a.put(t, "bad", "payload")
			a.setIntercept(engCorruptBlob("bad", tc.mutate))

			peer := b.addPeer(t, a)
			b.mustPull(t, peer) // a poisoned unit never fails the pull

			b.mustRead(t, "good", "fine")
			b.mustBeAbsent(t, "bad")
			b.mustHaveNoBlob(t, "payload")

			if b.hasEntry(t, "bad") {
				t.Error("the rejected unit is in b's log")
			}

			got := b.peer(t, peer)
			engEqual(t, "applied_total", got.AppliedTotal, int64(1))
			engEqual(t, "cursor", got.CursorSeq, a.state(t).HeadSeq)
			engEqual(t, "deferred units", got.DeferredUnits, 1)

			d := b.deferred(t, peer)
			if len(d) != 1 || d[0].UnitKey != "bad" || d[0].Reason != "digest_mismatch" || d[0].Attempts != 1 || !strings.Contains(d[0].LastError, engSHA("payload")) {
				t.Fatalf("deferred = %+v, want bad parked with digest_mismatch", d)
			}

			// The origin serves the right bytes again: the retry applies it and the park clears.
			a.setIntercept(nil)
			b.makeDue(t, peer)
			b.mustPull(t, peer)

			b.mustRead(t, "bad", "payload")
			engEqual(t, "deferred after the retry", len(b.deferred(t, peer)), 0)
		})
	}
}

func TestEngineBlobServerErrorIsAPeerErrorAndResumes(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "u1", "one")
	a.put(t, "u2", "two")
	a.put(t, "u3", "three")
	a.setIntercept(engFailBlob("u2"))

	peer := b.addPeer(t, a)

	err := b.pull(t, peer)

	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusInternalServerError {
		t.Fatalf("pull error = %v, want an HTTP 500 error", err)
	}

	got := b.peer(t, peer)
	engEqual(t, "applied_total", got.AppliedTotal, int64(1))
	engEqual(t, "cursor", got.CursorSeq, a.entry(t, "u1").Seq)
	engEqual(t, "error count", got.ErrorCount, 1)

	if !strings.Contains(got.LastError, "500") || got.NextAttemptAt == nil || !got.NextAttemptAt.After(time.Now()) || got.State() != PeerError {
		t.Errorf("peer = state %s, error %q, next attempt %v; want an error with a backoff", got.State(), got.LastError, got.NextAttemptAt)
	}

	b.mustRead(t, "u1", "one")
	b.mustBeAbsent(t, "u2")
	b.mustBeAbsent(t, "u3")
	b.mustHaveNoBlob(t, "two")

	a.setIntercept(nil)
	b.mustPull(t, peer)

	got = b.peer(t, peer)
	engEqual(t, "applied_total after recovery", got.AppliedTotal, int64(3))
	engEqual(t, "cursor after recovery", got.CursorSeq, a.state(t).HeadSeq)
	engEqual(t, "error count after recovery", got.ErrorCount, 0)

	if got.LastError != "" || got.NextAttemptAt != nil || got.State() != PeerOK {
		t.Errorf("peer after recovery = state %s, error %q, next attempt %v; want ok", got.State(), got.LastError, got.NextAttemptAt)
	}

	b.mustRead(t, "u2", "two")
	b.mustRead(t, "u3", "three")
}

func TestEngineBlobEndpointServesOnlyUnitOwnedBlobs(t *testing.T) {
	a := newEngSite(t, "site-a", "raw")
	otherID, otherToken := a.addRepo(t, "site-a-other")

	a.put(t, "one", "first")
	a.put(t, "two", "second")
	a.putIn(t, otherID, "theirs", "other-repo-secret")

	// A blob that exists in the store and the blobs table but backs no unit of the repository.
	seedBlob(t, a.db, "orphan")

	if _, err := a.blobs.Put(context.Background(), strings.NewReader("orphan"), nil); err != nil {
		t.Fatalf("store orphan: %v", err)
	}

	blobURL := func(unit, content string) string {
		return engBlobPath + "?unit=" + url.QueryEscape(unit) + "&sha256=" + engSHA(content)
	}

	tests := []struct {
		name   string
		path   string
		token  string
		status int
		body   string
	}{
		{"own blob", blobURL("one", "first"), a.token, http.StatusOK, "first"},
		{"another unit's blob", blobURL("one", "second"), a.token, http.StatusNotFound, ""},
		{"unknown unit", blobURL("nope", "first"), a.token, http.StatusNotFound, ""},
		{"blob of no unit", blobURL("one", "orphan"), a.token, http.StatusNotFound, ""},
		{"other repository's blob under its unit key", blobURL("theirs", "other-repo-secret"), a.token, http.StatusNotFound, ""},
		{"other repository's blob under an own unit key", blobURL("one", "other-repo-secret"), a.token, http.StatusNotFound, ""},
		{"other repository with its own token", blobURL("theirs", "other-repo-secret"), otherToken, http.StatusOK, "other-repo-secret"},
		{"malformed digest", engBlobPath + "?unit=one&sha256=../../etc/passwd", a.token, http.StatusBadRequest, ""},
		{"missing unit", engBlobPath + "?sha256=" + engSHA("first"), a.token, http.StatusBadRequest, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := engGet(t, a.srv.URL, tc.path, engBearer(tc.token))
			if status != tc.status {
				t.Fatalf("status = %d (%s), want %d", status, body, tc.status)
			}

			if tc.body != "" && string(body) != tc.body {
				t.Errorf("body = %q, want %q", body, tc.body)
			}

			if status != http.StatusOK && strings.Contains(string(body), "other-repo-secret") {
				t.Errorf("error body leaks content: %s", body)
			}
		})
	}
}

func TestEngineForgedBlobListIsNeverFetchedFromAPeer(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	otherID, _ := a.addRepo(t, "site-a-other")

	a.put(t, "victim", "harmless")
	a.putIn(t, otherID, "secret", "not-for-this-repo")

	secret := engSHA("not-for-this-repo")

	// The peer's unit answer names a blob that belongs to no unit of the pulled repository.
	a.setIntercept(engRewrite(engUnitPath, func(r *http.Request, doc map[string]any) {
		if r.URL.Query().Get("key") != "victim" {
			return
		}

		content, _ := doc["content"].(map[string]any)
		blobs, _ := content["blobs"].([]any)
		content["blobs"] = append(blobs, map[string]any{"sha256": secret, "size": 17})
	}))

	peer := b.addPeer(t, a)
	// The peer answers 404 for the foreign blob: the unit is parked, the pull goes on.
	if err := b.pull(t, peer); err != nil {
		t.Fatalf("pull error = %v, want the unit deferred", err)
	}

	d := b.deferred(t, peer)
	if len(d) != 1 || d[0].UnitKey != "victim" || d[0].Reason != "blob_missing" {
		t.Fatalf("deferred = %+v, want victim parked as blob_missing", d)
	}

	b.mustBeAbsent(t, "victim")
	b.mustHaveNoBlob(t, "not-for-this-repo")
	b.mustHaveNoBlob(t, "harmless")
	engEqual(t, "applied_total", b.peer(t, peer).AppliedTotal, int64(0))
}

// ---------------------------------------------------------------------------
// 5. Authentication

func TestEngineEndpointsRefuseMissingInvalidAndRevokedTokens(t *testing.T) {
	a := newEngSite(t, "site-a", "raw")
	a.put(t, "x", "x")

	ctx := context.Background()

	revoked, revokedPlain, err := a.store.CreateToken(ctx, a.repoID, "revoked", "test")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	if _, _, _, ok := engTokenWorks(t, a, revokedPlain); !ok {
		t.Fatal("a fresh token is refused")
	}

	if err := a.store.RevokeToken(ctx, a.repoID, revoked.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	deletedID, deletedToken := a.addRepo(t, "site-a-deleted")
	a.exec(t, `UPDATE repositories SET deleted_at = now() WHERE id = $1`, deletedID)

	disabledID, disabledToken := a.addRepo(t, "site-a-disabled")
	if err := a.store.Disable(ctx, disabledID); err != nil {
		t.Fatalf("disable: %v", err)
	}

	unknown, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	endpoints := []string{
		engInfoPath,
		engLogPath + "?after=0",
		engUnitPath + "?key=x",
		engBlobPath + "?unit=x&sha256=" + engSHA("x"),
	}
	refused := map[string]string{
		"malformed token":           engBearer("nope"),
		"unknown token":             engBearer(unknown),
		"revoked token":             engBearer(revokedPlain),
		"repository soft-deleted":   engBearer(deletedToken),
		"federation disabled":       engBearer(disabledToken),
		"valid token, wrong scheme": "Token " + a.token,
	}

	bodies := map[string]bool{}

	for _, ep := range endpoints {
		status, _, body := engGet(t, a.srv.URL, ep, "")
		if status != http.StatusUnauthorized || engErrCode(t, body) != codeUnauthorized {
			t.Errorf("%s without a token: %d %s, want 401 %s", ep, status, body, codeUnauthorized)
		}

		for name, header := range refused {
			status, hdr, body := engGet(t, a.srv.URL, ep, header)
			if status != http.StatusUnauthorized || engErrCode(t, body) != codeUnauthorized {
				t.Errorf("%s with %s: %d %s, want 401 %s", ep, name, status, body, codeUnauthorized)
			}

			if hdr.Get("Cache-Control") != "no-store" {
				t.Errorf("%s with %s: Cache-Control = %q, want no-store", ep, name, hdr.Get("Cache-Control"))
			}

			// "valid token, wrong scheme" is a missing token; every other refusal looks alike.
			if name != "valid token, wrong scheme" {
				bodies[string(body)] = true
			}
		}
	}

	if len(bodies) != 1 {
		t.Errorf("refusals differ (%d distinct bodies), a probe could tell the reasons apart: %v", len(bodies), bodies)
	}

	for _, ep := range endpoints[:2] {
		if status, _, body := engGet(t, a.srv.URL, ep, engBearer(a.token)); status != http.StatusOK {
			t.Errorf("%s with the valid token: %d %s", ep, status, body)
		}
	}
}

// engTokenWorks calls /info with the token.
func engTokenWorks(t *testing.T, s *engSite, token string) (int, http.Header, []byte, bool) {
	t.Helper()

	status, hdr, body := engGet(t, s.srv.URL, engInfoPath, engBearer(token))

	return status, hdr, body, status == http.StatusOK
}

func TestEngineTokenIsBoundToItsRepository(t *testing.T) {
	a := newEngSite(t, "site-a", "raw")
	yID, yToken := a.addRepo(t, "site-a-y")

	a.put(t, "x-only", "x")
	a.putIn(t, yID, "y-only", "y")

	type infoDoc struct {
		Repository string `json:"repository"`
	}

	type logDoc struct {
		Entries []Entry `json:"entries"`
	}

	tests := []struct {
		name  string
		token string
		repo  string
		own   string
		other string
	}{
		{"token of x", a.token, "site-a", "x-only", "y-only"},
		{"token of y", yToken, "site-a-y", "y-only", "x-only"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var info infoDoc

			status, _, body := engGet(t, a.srv.URL, engInfoPath, engBearer(tc.token))
			if err := json.Unmarshal(body, &info); status != http.StatusOK || err != nil || info.Repository != tc.repo {
				t.Fatalf("info = %d %s (%v), want repository %s", status, body, err, tc.repo)
			}

			var lg logDoc

			status, _, body = engGet(t, a.srv.URL, engLogPath+"?after=0", engBearer(tc.token))
			if err := json.Unmarshal(body, &lg); status != http.StatusOK || err != nil {
				t.Fatalf("log = %d %s (%v)", status, body, err)
			}

			if len(lg.Entries) != 1 || lg.Entries[0].UnitKey != tc.own {
				t.Errorf("log entries = %+v, want only %s", lg.Entries, tc.own)
			}

			if status, _, _ := engGet(t, a.srv.URL, engUnitPath+"?key="+tc.own, engBearer(tc.token)); status != http.StatusOK {
				t.Errorf("own unit: %d", status)
			}

			if status, _, body := engGet(t, a.srv.URL, engUnitPath+"?key="+tc.other, engBearer(tc.token)); status != http.StatusNotFound {
				t.Errorf("other repository's unit: %d %s, want 404", status, body)
			}
		})
	}

	tokens, err := a.store.ListTokens(context.Background(), a.repoID)
	if err != nil || len(tokens) != 1 || tokens[0].LastUsedAt == nil {
		t.Errorf("tokens = %+v (%v), want the used token's last_used_at set", tokens, err)
	}
}

func TestEnginePeerWithRevokedTokenFailsUntilItIsReplaced(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	a.put(t, "f", "payload")

	ctx := context.Background()
	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	tokens, err := a.store.ListTokens(ctx, a.repoID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens = %v, %v", tokens, err)
	}

	if err := a.store.RevokeToken(ctx, a.repoID, tokens[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	a.put(t, "g", "more")

	err = b.pull(t, peer)

	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnauthorized || !he.Fatal() {
		t.Fatalf("pull error = %v, want a fatal 401", err)
	}

	got := b.peer(t, peer)
	if got.ErrorCount != 1 || !strings.Contains(got.LastError, "401") || got.State() != PeerError {
		t.Errorf("peer = state %s, errors %d, last error %q", got.State(), got.ErrorCount, got.LastError)
	}

	b.mustBeAbsent(t, "g")

	_, fresh, err := a.store.CreateToken(ctx, a.repoID, "replacement", "test")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	if _, err := b.store.UpdatePeer(ctx, b.repoID, peer.ID, PeerPatch{Token: &fresh}); err != nil {
		t.Fatalf("update peer: %v", err)
	}

	engEqual(t, "error count after the token change", b.peer(t, peer).ErrorCount, 0)
	b.mustPull(t, peer)
	b.mustRead(t, "g", "more")
}

// ---------------------------------------------------------------------------
// 6. Quarantine

// setQuarantine records (or clears) a quarantining scan result for path.
func (s *engSite) setQuarantine(t *testing.T, path string, on bool) {
	t.Helper()

	ctx := context.Background()

	var refID, jobID string

	err := s.db.QueryRowContext(ctx,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'raw')
		 ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format RETURNING id::text`,
		s.repoID, path).Scan(&refID)
	if err != nil {
		t.Fatalf("artifact ref: %v", err)
	}

	err = s.db.QueryRowContext(ctx,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status) VALUES ($1, 'vuln', 'test', 'completed')
		 ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET status = 'completed' RETURNING id::text`,
		refID).Scan(&jobID)
	if err != nil {
		t.Fatalf("scan job: %v", err)
	}

	s.exec(t, `INSERT INTO vuln_results (scan_job_id, quarantined) VALUES ($1, $2)
	           ON CONFLICT (scan_job_id) DO UPDATE SET quarantined = EXCLUDED.quarantined`, jobID, on)
}

func TestEngineQuarantinedUnitIsDeferredUntilReleased(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "ok", "fine")
	a.put(t, "secret.bin", "payload")
	a.setQuarantine(t, "secret.bin", true)

	for _, ep := range []string{engUnitPath + "?key=secret.bin", engBlobPath + "?unit=secret.bin&sha256=" + engSHA("payload")} {
		status, _, body := engGet(t, a.srv.URL, ep, engBearer(a.token))
		if status != http.StatusLocked || engErrCode(t, body) != codeQuarantined {
			t.Errorf("%s: %d %s, want 423 %s", ep, status, body, codeQuarantined)
		}
	}

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	b.mustRead(t, "ok", "fine")
	b.mustBeAbsent(t, "secret.bin")
	b.mustHaveNoBlob(t, "payload")

	if b.hasEntry(t, "secret.bin") {
		t.Error("the quarantined unit is in b's log")
	}

	got := b.peer(t, peer)
	engEqual(t, "applied_total", got.AppliedTotal, int64(1))
	engEqual(t, "cursor", got.CursorSeq, a.state(t).HeadSeq)
	engEqual(t, "peer error count", got.ErrorCount, 0)

	d := b.deferred(t, peer)
	if len(d) != 1 || d[0].UnitKey != "secret.bin" || d[0].Reason != "quarantined" || d[0].Attempts != 1 {
		t.Fatalf("deferred = %+v, want secret.bin parked as quarantined", d)
	}

	// Still quarantined on the retry: parked again, with a later retry time.
	first := d[0].NextAttemptAt

	b.makeDue(t, peer)
	b.mustPull(t, peer)

	d = b.deferred(t, peer)
	if len(d) != 1 || d[0].Attempts != 2 || !d[0].NextAttemptAt.After(first) || !d[0].NextAttemptAt.After(time.Now()) {
		t.Fatalf("deferred after a retry = %+v, want attempts 2 and a later retry", d)
	}

	b.mustBeAbsent(t, "secret.bin")

	// Released at the origin: the next retry applies it and the park clears.
	a.setQuarantine(t, "secret.bin", false)
	b.makeDue(t, peer)
	b.mustPull(t, peer)

	b.mustRead(t, "secret.bin", "payload")
	engEqual(t, "deferred after release", len(b.deferred(t, peer)), 0)
	engEqual(t, "deferred units on the peer", b.peer(t, peer).DeferredUnits, 0)
	engEqual(t, "stamp", b.entry(t, "secret.bin").Stamp, a.entry(t, "secret.bin").Stamp)
}

// ---------------------------------------------------------------------------
// 7. Resync and log reset

func TestEngineFullPassTriggers(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(t *testing.T, a, b *engSite, peer *Peer)
		fullPass bool
	}{
		{"no reset", func(*testing.T, *engSite, *engSite, *Peer) {}, false},
		{"resync requested", func(t *testing.T, _, b *engSite, p *Peer) {
			t.Helper()

			if err := b.store.RequestResync(context.Background(), b.repoID, p.ID); err != nil {
				t.Fatalf("request resync: %v", err)
			}

			if !b.peer(t, p).ResyncRequested {
				t.Fatal("resync flag not set")
			}
		}, true},
		{"log identity changed", func(t *testing.T, a, _ *engSite, _ *Peer) {
			t.Helper()

			a.exec(t, `UPDATE federation_repos SET log_id = gen_random_uuid() WHERE repo_id = $1`, a.repoID)
		}, true},
		{"cursor beyond the peer's head", func(t *testing.T, _, b *engSite, p *Peer) {
			t.Helper()

			b.exec(t, `UPDATE federation_peers SET cursor_seq = cursor_seq + 50 WHERE id = $1::uuid`, p.ID)
		}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

			a.put(t, "one", "first")
			a.put(t, "two", "second")

			peer := b.addPeer(t, a)
			b.mustPull(t, peer)
			engEqual(t, "initial from-zero log requests", a.count(engLogPath, engFromZero), 1)

			tc.mutate(t, a, b, peer)
			a.put(t, "three", "third")
			a.resetRequests()
			b.mustPull(t, peer)

			wantFull := 0
			if tc.fullPass {
				wantFull = 1
			}

			engEqual(t, "from-zero log requests", a.count(engLogPath, engFromZero), wantFull)

			got := b.peer(t, peer)
			engEqual(t, "cursor", got.CursorSeq, a.state(t).HeadSeq)
			engEqual(t, "remote log id", *got.RemoteLogID, a.state(t).LogID)
			engEqual(t, "resync flag", got.ResyncRequested, false)
			engEqual(t, "error count", got.ErrorCount, 0)

			// The pass merged: only the new unit was fetched and applied.
			engEqual(t, "applied_total", got.AppliedTotal, int64(3))
			engEqual(t, "units fetched", a.count(engUnitPath, nil), 1)
			engEqual(t, "b head_seq", b.state(t).HeadSeq, int64(3))
			engEqual(t, "logs", b.logView(t, false), a.logView(t, false))
			b.mustRead(t, "three", "third")
		})
	}
}

func TestEnginePeerLogIdentityChangeDuringAPullFails(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	a.put(t, "one", "first")

	a.setIntercept(engRewrite(engLogPath, func(_ *http.Request, doc map[string]any) { doc["log_id"] = uuid.NewString() }))

	peer := b.addPeer(t, a)
	err := b.pull(t, peer)

	if !errors.Is(err, errPeerMismatch) || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("pull error = %v, want the log identity change refused", err)
	}

	b.mustBeAbsent(t, "one")
	engEqual(t, "applied_total", b.peer(t, peer).AppliedTotal, int64(0))
	engEqual(t, "error count", b.peer(t, peer).ErrorCount, 1)
}

func TestEngineBatchedPullPagesThroughTheLog(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	b.eng.BatchSize = 2

	for i := range 5 {
		a.put(t, fmt.Sprintf("f%d", i), fmt.Sprintf("content %d", i))
	}

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	engEqual(t, "log pages", a.count(engLogPath, nil), 3)
	engEqual(t, "applied_total", b.peer(t, peer).AppliedTotal, int64(5))
	engEqual(t, "cursor", b.peer(t, peer).CursorSeq, a.state(t).HeadSeq)
	engEqual(t, "logs", b.logView(t, false), a.logView(t, false))
}

// ---------------------------------------------------------------------------
// 8. Quota

func TestEngineQuotaExceededDefersUnitAndLeavesUsageAlone(t *testing.T) {
	tests := []struct {
		name     string
		maxSize  int64
		maxCount int64
	}{
		{"artifact count limit", 0, 1},
		{"size limit", 5, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
			b.exec(t, `INSERT INTO storage_quotas (repository_id, max_size_bytes, max_artifact_count) VALUES ($1, $2, $3)`, b.repoID, tc.maxSize, tc.maxCount)

			a.put(t, "u1", "aaaa")
			a.put(t, "u2", "bbbbbbbb")

			peer := b.addPeer(t, a)
			b.mustPull(t, peer)

			b.mustRead(t, "u1", "aaaa")
			b.mustBeAbsent(t, "u2")
			b.mustHaveNoBlob(t, "bbbbbbbb")
			b.wantQuota(t, 4, 1)

			got := b.peer(t, peer)
			engEqual(t, "applied_total", got.AppliedTotal, int64(1))
			engEqual(t, "cursor", got.CursorSeq, a.state(t).HeadSeq)
			engEqual(t, "error count", got.ErrorCount, 0)

			d := b.deferred(t, peer)
			if len(d) != 1 || d[0].UnitKey != "u2" || d[0].Reason != "quota_exceeded" || !strings.Contains(d[0].LastError, "quota") {
				t.Fatalf("deferred = %+v, want u2 parked as quota_exceeded", d)
			}

			// Retrying under the same limit leaves usage where it was.
			b.makeDue(t, peer)
			b.mustPull(t, peer)
			b.wantQuota(t, 4, 1)
			engEqual(t, "deferred attempts", b.deferred(t, peer)[0].Attempts, 2)

			// Raise the limit: the retry applies it and the park clears.
			b.exec(t, `UPDATE storage_quotas SET max_size_bytes = 0, max_artifact_count = 0 WHERE repository_id = $1`, b.repoID)
			b.makeDue(t, peer)
			b.mustPull(t, peer)

			b.mustRead(t, "u2", "bbbbbbbb")
			b.wantQuota(t, 12, 2)
			engEqual(t, "deferred after raising the quota", len(b.deferred(t, peer)), 0)
		})
	}
}

func TestEngineFailedApplyReleasesQuotaAndIsParkedAfterThreePulls(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	// The unit named boom cannot be written at b, whatever the peer sends.
	b.exec(t, `CREATE FUNCTION eng_poison() RETURNS trigger LANGUAGE plpgsql AS $$
	           BEGIN IF NEW.path = 'boom' THEN RAISE EXCEPTION 'poison unit'; END IF; RETURN NEW; END $$`)
	b.exec(t, `CREATE TRIGGER eng_poison BEFORE INSERT ON raw_artifacts FOR EACH ROW EXECUTE FUNCTION eng_poison()`)

	a.put(t, "fine", "fine!")
	a.put(t, "boom", "boom-content")
	a.put(t, "after", "after")

	peer := b.addPeer(t, a)

	for attempt := 1; attempt <= 2; attempt++ {
		err := b.pull(t, peer)
		if err == nil || !strings.Contains(err.Error(), "poison unit") {
			t.Fatalf("pull %d error = %v, want the apply failure", attempt, err)
		}

		got := b.peer(t, peer)
		engEqual(t, fmt.Sprintf("error count after pull %d", attempt), got.ErrorCount, attempt)
		engEqual(t, fmt.Sprintf("cursor after pull %d", attempt), got.CursorSeq, a.entry(t, "fine").Seq)
		engEqual(t, fmt.Sprintf("deferred after pull %d", attempt), len(b.deferred(t, peer)), 0)
		b.mustBeAbsent(t, "after")

		// The reservation made for the failed unit went back.
		b.wantQuota(t, 5, 1)
	}

	b.mustPull(t, peer)

	d := b.deferred(t, peer)
	if len(d) != 1 || d[0].UnitKey != "boom" || d[0].Reason != "apply_failed" || !strings.Contains(d[0].LastError, "poison unit") {
		t.Fatalf("deferred = %+v, want boom parked as apply_failed", d)
	}

	b.mustRead(t, "after", "after")
	b.mustBeAbsent(t, "boom")
	b.wantQuota(t, 10, 2)

	got := b.peer(t, peer)
	engEqual(t, "error count after parking", got.ErrorCount, 0)
	engEqual(t, "cursor after parking", got.CursorSeq, a.state(t).HeadSeq)
}

func TestEngineQuotaFollowsReplacedAndDeletedUnits(t *testing.T) {
	tests := []struct {
		name      string
		retention int
		size      int64
		count     int64
	}{
		{"deleted unit stays in the trash and keeps counting", 7, 10, 1},
		{"deleted unit without a trash is released", 0, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
			b.exec(t, `UPDATE repositories SET trash_retention_days = $2 WHERE id = $1`, b.repoID, tc.retention)

			a.put(t, "f", "12345")

			peer := b.addPeer(t, a)
			b.mustPull(t, peer)
			b.wantQuota(t, 5, 1)

			// A replaced unit swaps its usage: the new size is reserved, the old one returned.
			a.put(t, "f", "1234567890")
			b.mustPull(t, peer)
			b.mustRead(t, "f", "1234567890")
			b.wantQuota(t, 10, 1)

			if _, inTable := b.hasBlob(t, engSHA("12345")); inTable {
				t.Error("the replaced blob is still referenced")
			}

			a.purge(t, "f")
			b.mustPull(t, peer)
			b.mustBeAbsent(t, "f")
			b.wantQuota(t, tc.size, tc.count)
		})
	}
}

// ---------------------------------------------------------------------------
// 9. Refused peers

func TestEngineRefusesAPeerThatIsThisInstance(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, a, b *engSite) (puller, source *engSite)
	}{
		{"peer URL points at the same server", func(_ *testing.T, a, _ *engSite) (*engSite, *engSite) { return a, a }},
		{"clone with the same instance id", func(t *testing.T, a, b *engSite) (*engSite, *engSite) {
			t.Helper()

			b.exec(t, `UPDATE federation_instance SET instance_id = $1`, a.instance(t))
			b.newEngine(t)

			return b, a
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
			a.put(t, "f", "payload")
			b.put(t, "g", "local")

			puller, source := tc.setup(t, a, b)
			logBefore, liveBefore := puller.logView(t, true), puller.live(t)

			peer := puller.addPeer(t, source)
			err := puller.pull(t, peer)

			if !errors.Is(err, errPeerMismatch) || !strings.Contains(err.Error(), "the peer is this instance") {
				t.Fatalf("pull error = %v, want the self peer refused", err)
			}

			got := puller.peer(t, peer)
			if got.ErrorCount != 1 || !strings.Contains(got.LastError, "this instance") || got.State() != PeerError || got.AppliedTotal != 0 {
				t.Errorf("peer = state %s, errors %d, last error %q, applied %d", got.State(), got.ErrorCount, got.LastError, got.AppliedTotal)
			}

			engEqual(t, "log pages requested", source.count(engLogPath, nil), 0)
			engEqual(t, "log", puller.logView(t, true), logBefore)
			engEqual(t, "live rows", puller.live(t), liveBefore)
		})
	}
}

func TestEngineRefusesFormatAndProtocolMismatch(t *testing.T) {
	tests := []struct {
		name   string
		source func(t *testing.T) *engSite
		want   string
	}{
		{"repository format differs", func(t *testing.T) *engSite {
			t.Helper()

			return newEngSite(t, "site-c", "maven")
		}, "peer repository is maven, local is raw"},
		{"protocol version differs", func(t *testing.T) *engSite {
			t.Helper()

			c := newEngSite(t, "site-c", "raw")
			c.put(t, "f", "payload")
			c.setIntercept(engRewrite(engInfoPath, func(_ *http.Request, doc map[string]any) { doc["protocol_version"] = 99 }))

			return c
		}, "peer speaks protocol 99"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newEngSite(t, "site-b", "raw")
			b.put(t, "g", "local")

			c := tc.source(t)
			logBefore, liveBefore := b.logView(t, true), b.live(t)

			peer := b.addPeer(t, c)
			err := b.pull(t, peer)

			if !errors.Is(err, errPeerMismatch) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pull error = %v, want %q", err, tc.want)
			}

			got := b.peer(t, peer)
			if got.ErrorCount != 1 || !strings.Contains(got.LastError, tc.want) || got.AppliedTotal != 0 {
				t.Errorf("peer = errors %d, last error %q, applied %d", got.ErrorCount, got.LastError, got.AppliedTotal)
			}

			engEqual(t, "log pages requested", c.count(engLogPath, nil), 0)
			engEqual(t, "log", b.logView(t, true), logBefore)
			engEqual(t, "live rows", b.live(t), liveBefore)
		})
	}
}

// ---------------------------------------------------------------------------
// 10. Tombstone garbage collection

func TestEngineTombstoneGCRemovesOnlyExpiredTombstones(t *testing.T) {
	a := newEngSite(t, "site-a", "raw")
	otherID, _ := a.addRepo(t, "site-a-other")

	day := func(n int) string { return fmt.Sprintf("now() - interval '%d days'", n) }
	age := func(repoID, key string, days int) {
		a.exec(t, `UPDATE federation_units SET changed_at = `+day(days)+` WHERE repo_id = $1 AND unit_key = $2`, repoID, key)
	}

	a.put(t, "live-old", "1")
	a.put(t, "live-new", "2")
	a.put(t, "dead-old", "3")
	a.put(t, "dead-edge", "4")
	a.put(t, "dead-new", "5")
	a.putIn(t, otherID, "other-dead-old", "6")

	for _, k := range []string{"dead-old", "dead-edge", "dead-new"} {
		a.trash(t, k)
	}

	a.exec(t, `DELETE FROM raw_artifacts WHERE repository_id = $1 AND path = 'other-dead-old'`, otherID)

	age(a.repoID, "live-old", 90)
	age(a.repoID, "dead-old", 40)
	age(a.repoID, "dead-edge", 29)
	age(otherID, "other-dead-old", 400)

	headBefore := a.state(t).HeadSeq
	a.eng.TombstoneRetention = 30 * 24 * time.Hour

	// Another replica is running the collection: this one skips it.
	release, locked, err := tryLock(context.Background(), a.db, "federation:gc")
	if err != nil || !locked {
		t.Fatalf("take the gc lock: %v (locked %v)", err, locked)
	}

	a.eng.purgeTombstones(context.Background())

	if !a.hasEntry(t, "dead-old") {
		t.Fatal("tombstone collected while another replica holds the lock")
	}

	release()

	a.eng.purgeTombstones(context.Background())

	for key, want := range map[string]bool{
		"live-old": true, "live-new": true, "dead-old": false, "dead-edge": true, "dead-new": true,
	} {
		if got := a.hasEntry(t, key); got != want {
			t.Errorf("unit %s logged = %v, want %v", key, got, want)
		}
	}

	if _, err := a.store.GetEntry(context.Background(), a.db, otherID, "other-dead-old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired tombstone of the other repository: err = %v, want collected", err)
	}

	for _, key := range []string{"dead-edge", "dead-new"} {
		if !a.entry(t, key).Tombstone {
			t.Errorf("%s lost its tombstone flag", key)
		}
	}

	engEqual(t, "head_seq", a.state(t).HeadSeq, headBefore)

	// Nothing else is due, and a shorter retention collects the rest.
	n, err := a.store.PurgeTombstones(context.Background(), 30*24*time.Hour)
	engEqual(t, "second collection", n, int64(0))

	if err != nil {
		t.Fatal(err)
	}

	n, err = a.store.PurgeTombstones(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	engEqual(t, "collection at 7 days", n, int64(1))
	engEqual(t, "live units", a.live(t), map[string]string{"live-old": engSHA("1"), "live-new": engSHA("2")})
}

// ---------------------------------------------------------------------------
// The worker

func TestEngineWorkerPullsPeersAndCollectsTombstones(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "w1", "first")

	b.put(t, "stale", "x")
	b.trash(t, "stale")
	b.exec(t, `UPDATE federation_units SET changed_at = now() - interval '90 days' WHERE repo_id = $1 AND unit_key = 'stale'`, b.repoID)

	peer := b.addPeer(t, a)
	b.eng.PollInterval = 50 * time.Millisecond

	w := b.eng.StartWorker(context.Background())
	t.Cleanup(w.Stop)

	engEventually(t, "w1 to be pulled", func() bool { _, ok := b.read(t, "w1"); return ok })
	engEventually(t, "the expired tombstone to be collected", func() bool { return !b.hasEntry(t, "stale") })

	// A change made while the worker runs is picked up on a later cycle.
	a.put(t, "w2", "second")
	engEventually(t, "w2 to be pulled", func() bool { _, ok := b.read(t, "w2"); return ok })

	w.Stop()

	got := b.peer(t, peer)
	engEqual(t, "applied_total", got.AppliedTotal, int64(2))
	engEqual(t, "cursor", got.CursorSeq, a.state(t).HeadSeq)
	engEqual(t, "error count", got.ErrorCount, 0)
}

// ---------------------------------------------------------------------------
// Panic recovery

// engPanicCodec wraps the codec of a format and panics in Import while armed.
type engPanicCodec struct {
	Codec

	armed atomic.Bool
}

func (c *engPanicCodec) Import(ctx context.Context, tx *sql.Tx, repoID, key string, content *Content, blobIDs map[string]string) (*Change, error) {
	if c.armed.Load() {
		panic("injected codec panic")
	}

	return c.Codec.Import(ctx, tx, repoID, key, content, blobIDs)
}

// engInjectPanics swaps the codec of format for a panicking wrapper (armed)
// until the test ends.
func engInjectPanics(t *testing.T, format string) *engPanicCodec {
	t.Helper()

	orig := codecs[format]
	pc := &engPanicCodec{Codec: orig}
	pc.armed.Store(true)
	codecs[format] = pc

	t.Cleanup(func() { codecs[format] = orig })

	return pc
}

func TestEnginePanicInAPullIsRecordedAsPeerErrorAndTheEngineRecovers(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "u1", "one")

	pc := engInjectPanics(t, "raw")
	peer := b.addPeer(t, a)

	err := b.pull(t, peer)
	if !errors.Is(err, errPullPanic) || !strings.Contains(err.Error(), "injected codec panic") {
		t.Fatalf("pull error = %v, want the recovered panic", err)
	}

	got := b.peer(t, peer)
	engEqual(t, "error count", got.ErrorCount, 1)

	if !strings.Contains(got.LastError, "panicked") || !strings.Contains(got.LastError, "injected codec panic") ||
		got.NextAttemptAt == nil || got.State() != PeerError {
		t.Errorf("peer = state %s, error %q, next attempt %v; want an error naming the panic, with a backoff", got.State(), got.LastError, got.NextAttemptAt)
	}

	b.mustBeAbsent(t, "u1")

	// The same engine pulls again once the cause is gone.
	pc.armed.Store(false)
	b.mustPull(t, peer)

	got = b.peer(t, peer)
	engEqual(t, "error count after recovery", got.ErrorCount, 0)

	if got.LastError != "" || got.State() != PeerOK {
		t.Errorf("peer after recovery = state %s, error %q; want ok", got.State(), got.LastError)
	}

	b.mustRead(t, "u1", "one")
}

func TestEngineWorkerSurvivesAPanickingPullAndKeepsPulling(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "w1", "first")

	pc := engInjectPanics(t, "raw")
	peer := b.addPeer(t, a)
	b.eng.PollInterval = 50 * time.Millisecond

	w := b.eng.StartWorker(context.Background())
	t.Cleanup(w.Stop)

	engEventually(t, "the panic to be recorded on the peer", func() bool {
		p := b.peer(t, peer)

		return p.State() == PeerError && strings.Contains(p.LastError, "injected codec panic")
	})

	pc.armed.Store(false)

	engEventually(t, "the worker to pull after the panic", func() bool { _, ok := b.read(t, "w1"); return ok })
	engEventually(t, "the peer to be healthy again", func() bool { return b.peer(t, peer).State() == PeerOK })

	w.Stop()
}

// ---------------------------------------------------------------------------
// npm: a dist-tag pulled before its version brings the version along

func TestEngineNPMTagPulledBeforeItsVersionAppliesTheVersionFirst(t *testing.T) {
	a, b := newEngSite(t, "npm-a", "npm"), newEngSite(t, "npm-b", "npm")

	tarballs := []string{"tarball widget 1.0.0", "tarball widget 1.1.0", "tarball plain 2.0.0"}
	for _, c := range tarballs {
		if _, err := a.blobs.Put(context.Background(), strings.NewReader(c), nil); err != nil {
			t.Fatalf("store tarball: %v", err)
		}
	}

	seedNPMContent(t, a.db, a.repoID)

	// Changing the manifest of 1.1.0 moves its log entry behind the dist-tags that point at it.
	a.exec(t, `UPDATE npm_versions v SET manifest_json = manifest_json || '{"deprecated": "use 1.2"}'::jsonb
	             FROM npm_packages p WHERE p.id = v.package_id AND p.repository_id = $1 AND v.version = '1.1.0'`, a.repoID)

	if a.entry(t, npmTagNext).Seq > a.entry(t, npmVerKey2).Seq {
		t.Fatal("precondition: the tag must be logged before the version it points at")
	}

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	engEqual(t, "applied_total", b.peer(t, peer).AppliedTotal > 0, true)
	engEqual(t, "error count", b.peer(t, peer).ErrorCount, 0)
	engEqual(t, "deferred", len(b.deferred(t, peer)), 0)
	engEqual(t, "logs", b.logView(t, false), a.logView(t, false))

	tags := map[string]string{}

	rows, err := b.db.QueryContext(context.Background(),
		`SELECT p.full_name || '#' || d.tag, d.version FROM npm_dist_tags d JOIN npm_packages p ON p.id = d.package_id WHERE p.repository_id = $1`, b.repoID)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan tag: %v", err)
		}

		tags[k] = v
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("list tags: %v", err)
	}

	engEqual(t, "dist-tags", tags, map[string]string{npmTagKey: "1.0.0", npmTagNext: "1.1.0", npmPlainTag: "2.0.0"})
	engEqual(t, "versions", b.scalar(t,
		`SELECT count(*) FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id WHERE p.repository_id = $1`, b.repoID), int64(3))
	engEqual(t, "deprecation of 1.1.0", b.scalar(t,
		`SELECT count(*) FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id
		  WHERE p.repository_id = $1 AND v.version = '1.1.0' AND v.manifest_json->>'deprecated' = 'use 1.2'`, b.repoID), int64(1))

	for _, c := range tarballs {
		if inStore, inTable := b.hasBlob(t, engSHA(c)); !inStore || !inTable {
			t.Errorf("tarball %q missing at b (store %v, table %v)", c, inStore, inTable)
		}
	}

	if len(b.notes.npmPackages()) == 0 {
		t.Error("no packument regeneration was queued for the replicated packages")
	}
}
