// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

var fedAPIDBSeq atomic.Int64 //nolint:gochecknoglobals // unique database names within the test binary

// fedAPIPeerURL is a public IP literal: the SSRF guard accepts it without DNS.
const fedAPIPeerURL = "https://93.184.216.34"

// fedAPINewDB creates an empty migrated database on the TEST_DATABASE_URL server.
func fedAPINewDB(t *testing.T) *database.PostgresDB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	name := fmt.Sprintf("oa_fedapi_%d_%d", os.Getpid(), fedAPIDBSeq.Add(1))

	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	log, err := logger.New("warn", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: name, SSLMode: sslmode, MaxOpenConns: 10, MaxIdleConns: 2, ConnMaxLifetimeMinutes: 5,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return pg
}

// fedAPISeedRepo inserts a repository of the given type and format in the
// system project and returns its id.
func fedAPISeedRepo(t *testing.T, db *sql.DB, name, format, repoType string) string {
	t.Helper()

	var id string

	err := db.QueryRowContext(context.Background(),
		`INSERT INTO repositories (name, format, repo_type, description, project_id)
		 VALUES ($1, $2, $3, '', (SELECT id FROM projects ORDER BY created_at LIMIT 1)) RETURNING id::text`,
		name, format, repoType).Scan(&id)
	if err != nil {
		t.Fatalf("seed repository %s: %v", name, err)
	}

	return id
}

func fedAPILogEnabled(t *testing.T, db *sql.DB, repoID string) bool {
	t.Helper()

	var n int

	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM federation_repos WHERE repo_id = $1`, repoID).Scan(&n); err != nil {
		t.Fatalf("count federation_repos: %v", err)
	}

	return n > 0
}

// fedAPIEnv is a federation admin handler over a real database.
type fedAPIEnv struct {
	pg    *database.PostgresDB
	db    *sql.DB
	store *federation.Store
	log   *logger.Logger
	eval  *auth.Evaluator
	kicks atomic.Int32
	// router is rebuilt by use() when the server flag changes.
	router chi.Router
}

//nolint:gochecknoglobals // immutable principals shared by the table-driven tests
var (
	fedAPIAdmin = &auth.Principal{Username: "root", IsLegacy: true}
	fedAPIAnon  = &auth.Principal{IsAnonymous: true}
)

// fedAPIPlainUser is an authenticated user without any permission target.
func fedAPIPlainUser() *auth.Principal {
	id := uuid.NewString()

	return &auth.Principal{UserID: &id, Username: "plain"}
}

func newFedAPIEnv(t *testing.T, serverEnabled bool) *fedAPIEnv {
	t.Helper()

	pg := fedAPINewDB(t)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	e := &fedAPIEnv{pg: pg, db: pg.GetDB(), store: federation.NewStore(pg.GetDB()), log: log}
	e.eval = auth.NewEvaluator(pg, log, false)
	e.mount(serverEnabled)

	return e
}

func (e *fedAPIEnv) mount(serverEnabled bool) {
	h := NewFederationHandler(e.pg, e.log, e.eval, e.store, serverEnabled, func() { e.kicks.Add(1) })
	r := chi.NewRouter()
	h.RegisterAuthed(r)
	e.router = r
}

// do performs a request as p; body "" sends none. The decoded JSON object (nil
// when the body is empty or not an object) and the raw body are returned.
func (e *fedAPIEnv) do(t *testing.T, p *auth.Principal, method, path, body string) (int, map[string]any, string) {
	t.Helper()

	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}

	req := httptest.NewRequestWithContext(context.Background(), method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	rw := httptest.NewRecorder()
	e.router.ServeHTTP(rw, req)

	raw := rw.Body.String()

	var m map[string]any
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}

	return rw.Code, m, raw
}

func fedAPIPath(repo string, tail ...string) string {
	return "/api/v1/repositories/" + repo + "/federation" + strings.Join(tail, "")
}

// enable turns federation on for repo through the API and fails the test on error.
func (e *fedAPIEnv) enable(t *testing.T, repo string) {
	t.Helper()

	if code, _, raw := e.do(t, fedAPIAdmin, http.MethodPut, fedAPIPath(repo), `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enable federation: %d %s", code, raw)
	}
}

// addPeer creates a peer and returns its id.
func (e *fedAPIEnv) addPeer(t *testing.T, repo, name string) string {
	t.Helper()

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath(repo, "/peers"),
		fmt.Sprintf(`{"name":%q,"base_url":%q,"token":"peer-secret-token"}`, name, fedAPIPeerURL))
	if code != http.StatusCreated {
		t.Fatalf("create peer: %d %s", code, raw)
	}

	id, _ := m["id"].(string)

	return id
}

func TestFederationAuthorization(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")

	routes := []struct{ method, tail, body string }{
		{http.MethodGet, "", ""},
		{http.MethodPut, "", `{"enabled":true}`},
		{http.MethodPost, "/peers", `{}`},
		{http.MethodPatch, "/peers/x", `{}`},
		{http.MethodDelete, "/peers/x", ""},
		{http.MethodPost, "/peers/x/resync", ""},
		{http.MethodGet, "/peers/x/deferred", ""},
		{http.MethodPost, "/tokens", `{"name":"t"}`},
		{http.MethodDelete, "/tokens/x", ""},
	}

	callers := map[string]*auth.Principal{"anonymous": fedAPIAnon, "plain user": fedAPIPlainUser()}

	for who, p := range callers {
		for _, rt := range routes {
			t.Run(who+" "+rt.method+rt.tail, func(t *testing.T) {
				code, m, raw := e.do(t, p, rt.method, fedAPIPath("files", rt.tail), rt.body)
				if code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403; body %s", code, raw)
				}

				if m["code"] != "forbidden" {
					t.Errorf("code = %v, want forbidden", m["code"])
				}
			})
		}
	}

	if fedAPILogEnabled(t, e.db, fedAPIRepoID(t, e.db, "files")) {
		t.Error("an unauthorized PUT enabled federation")
	}
}

func fedAPIRepoID(t *testing.T, db *sql.DB, name string) string {
	t.Helper()

	var id string
	if err := db.QueryRowContext(context.Background(), `SELECT id::text FROM repositories WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("repository id: %v", err)
	}

	return id
}

func TestFederationUnknownResources(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	missing := uuid.NewString()

	tests := []struct {
		name         string
		method, path string
		body         string
		want         int
	}{
		{"status of unknown repo", http.MethodGet, fedAPIPath("ghost"), "", http.StatusNotFound},
		{"enable unknown repo", http.MethodPut, fedAPIPath("ghost"), `{"enabled":true}`, http.StatusNotFound},
		{"create peer on unknown repo", http.MethodPost, fedAPIPath("ghost", "/peers"), `{}`, http.StatusNotFound},
		{"unknown peer patch", http.MethodPatch, fedAPIPath("files", "/peers/"+missing), `{"enabled":false}`, http.StatusNotFound},
		{"unknown peer delete", http.MethodDelete, fedAPIPath("files", "/peers/"+missing), "", http.StatusNotFound},
		{"unknown peer resync", http.MethodPost, fedAPIPath("files", "/peers/"+missing+"/resync"), "", http.StatusNotFound},
		{"unknown peer deferred", http.MethodGet, fedAPIPath("files", "/peers/"+missing+"/deferred"), "", http.StatusNotFound},
		{"unknown token revoke", http.MethodDelete, fedAPIPath("files", "/tokens/"+missing), "", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, m, raw := e.do(t, fedAPIAdmin, tt.method, tt.path, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body %s", code, tt.want, raw)
			}

			if m["code"] != "not_found" {
				t.Errorf("code = %v, want not_found", m["code"])
			}
		})
	}
}

func TestFederationNotEnabledRoutesConflict(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")

	id := uuid.NewString()

	routes := []struct{ method, tail, body string }{
		{http.MethodPost, "/peers", `{"name":"p","base_url":"` + fedAPIPeerURL + `","token":"t"}`},
		{http.MethodPatch, "/peers/" + id, `{"enabled":false}`},
		{http.MethodDelete, "/peers/" + id, ""},
		{http.MethodPost, "/peers/" + id + "/resync", ""},
		{http.MethodGet, "/peers/" + id + "/deferred", ""},
		{http.MethodPost, "/tokens", `{"name":"t"}`},
		{http.MethodDelete, "/tokens/" + id, ""},
	}

	for _, rt := range routes {
		t.Run(rt.method+rt.tail, func(t *testing.T) {
			code, m, raw := e.do(t, fedAPIAdmin, rt.method, fedAPIPath("files", rt.tail), rt.body)
			if code != http.StatusConflict || m["code"] != "federation_not_enabled" {
				t.Fatalf("got %d %s, want 409 federation_not_enabled", code, raw)
			}
		})
	}
}

func TestFederationStatusAndEnableDisable(t *testing.T) {
	e := newFedAPIEnv(t, true)
	repoID := fedAPISeedRepo(t, e.db, "files", "raw", "hosted")

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, raw)
	}

	if m["enabled"] != false || m["supported"] != true || m["instance_id"] == "" {
		t.Errorf("fresh status = %v, want disabled+supported with an instance id", m)
	}

	if peers, ok := m["peers"].([]any); !ok || len(peers) != 0 {
		t.Errorf("peers = %v, want empty array", m["peers"])
	}

	if tokens, ok := m["tokens"].([]any); !ok || len(tokens) != 0 {
		t.Errorf("tokens = %v, want empty array", m["tokens"])
	}

	code, m, raw = e.do(t, fedAPIAdmin, http.MethodPut, fedAPIPath("files"), `{"enabled":true}`)
	if code != http.StatusOK || m["enabled"] != true || m["log_id"] == "" {
		t.Fatalf("enable: %d %s", code, raw)
	}

	if !fedAPILogEnabled(t, e.db, repoID) {
		t.Error("federation log not started")
	}

	code, m, raw = e.do(t, fedAPIAdmin, http.MethodPut, fedAPIPath("files"), `{"enabled":false}`)
	if code != http.StatusOK || m["enabled"] != false {
		t.Fatalf("disable: %d %s", code, raw)
	}

	if fedAPILogEnabled(t, e.db, repoID) {
		t.Error("federation log still enabled after disable")
	}
}

func TestFederationSetEnabledValidation(t *testing.T) {
	tests := []struct {
		name     string
		server   bool
		format   string
		repoType string
		body     string
		want     int
		wantCode string
	}{
		{"proxy repository", true, "raw", "proxy", `{"enabled":true}`, http.StatusBadRequest, "validation_failed"},
		{"group repository", true, "raw", "group", `{"enabled":true}`, http.StatusBadRequest, "validation_failed"},
		{"unsupported format", true, "deb", "hosted", `{"enabled":true}`, http.StatusBadRequest, "validation_failed"},
		{"server feature off", false, "raw", "hosted", `{"enabled":true}`, http.StatusConflict, "federation_disabled"},
		{"server feature off, unsupported repo", false, "deb", "hosted", `{"enabled":true}`, http.StatusConflict, "federation_disabled"},
		{"missing enabled", true, "raw", "hosted", `{}`, http.StatusBadRequest, "bad_request"},
		{"malformed JSON", true, "raw", "hosted", `{`, http.StatusBadRequest, "bad_request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFedAPIEnv(t, tt.server)
			repoID := fedAPISeedRepo(t, e.db, "target", tt.format, tt.repoType)

			code, m, raw := e.do(t, fedAPIAdmin, http.MethodPut, fedAPIPath("target"), tt.body)
			if code != tt.want || m["code"] != tt.wantCode {
				t.Fatalf("got %d %s, want %d %s", code, raw, tt.want, tt.wantCode)
			}

			if fedAPILogEnabled(t, e.db, repoID) {
				t.Error("federation log started despite the refusal")
			}
		})
	}
}

func TestFederationStatusServerDisabledNotSupported(t *testing.T) {
	e := newFedAPIEnv(t, false)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")
	if code != http.StatusOK || m["supported"] != false || m["enabled"] != false {
		t.Fatalf("status = %d %s, want supported=false enabled=false", code, raw)
	}
}

func TestFederationDisableAllowedWhenServerOff(t *testing.T) {
	e := newFedAPIEnv(t, true)
	repoID := fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")
	e.mount(false)

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPut, fedAPIPath("files"), `{"enabled":false}`)
	if code != http.StatusOK || m["enabled"] != false {
		t.Fatalf("disable with server off: %d %s", code, raw)
	}

	if fedAPILogEnabled(t, e.db, repoID) {
		t.Error("log still enabled")
	}
}

func TestFederationCreatePeerValidation(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "")

	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	tests := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{`},
		{"missing fields", `{"name":"p"}`},
		{"missing token", `{"name":"p","base_url":"` + fedAPIPeerURL + `"}`},
		{"http without allow-private", `{"name":"p","base_url":"http://93.184.216.34","token":"t"}`},
		{"userinfo", `{"name":"p","base_url":"https://user:pw@93.184.216.34","token":"t"}`},
		{"query string", `{"name":"p","base_url":"https://93.184.216.34/?a=b","token":"t"}`},
		{"not a url", `{"name":"p","base_url":"nonsense","token":"t"}`},
		{"loopback address", `{"name":"p","base_url":"https://127.0.0.1","token":"t"}`},
		{"bad name", `{"name":"-bad name","base_url":"` + fedAPIPeerURL + `","token":"t"}`},
		{"empty token", `{"name":"p","base_url":"` + fedAPIPeerURL + `","token":""}`},
		{"oversized token", `{"name":"p","base_url":"` + fedAPIPeerURL + `","token":"` + strings.Repeat("a", 513) + `"}`},
		{"invalid ca pem", `{"name":"p","base_url":"` + fedAPIPeerURL + `","token":"t","ca_pem":"not pem"}`},
		{"oversized pem", `{"name":"p","base_url":"` + fedAPIPeerURL + `","token":"t","ca_pem":"` + strings.Repeat("a", 64<<10+1) + `"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, m, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/peers"), tt.body)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", code, raw)
			}

			if c := m["code"]; c != "validation_failed" && c != "bad_request" {
				t.Errorf("code = %v", c)
			}
		})
	}

	_, st, _ := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")
	if peers, _ := st["peers"].([]any); len(peers) != 0 {
		t.Errorf("a rejected request created peers: %v", peers)
	}
}

func TestFederationCreatePeerHTTPAllowedWithPrivateUpstreams(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	code, _, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/peers"),
		`{"name":"lan","base_url":"http://127.0.0.1:9","token":"t"}`)
	if code != http.StatusCreated {
		t.Fatalf("http peer with private upstreams allowed: %d %s", code, raw)
	}
}

func TestFederationPeerLifecycleNeverEchoesSecrets(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	const secret = "peer-secret-token"

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/peers"),
		fmt.Sprintf(`{"name":"east","base_url":%q,"token":%q}`, fedAPIPeerURL, secret))
	if code != http.StatusCreated {
		t.Fatalf("create peer: %d %s", code, raw)
	}

	if strings.Contains(raw, secret) {
		t.Errorf("create response echoes the peer token: %s", raw)
	}

	for _, k := range []string{"token", "token_enc", "ca_pem", "client_cert", "client_key", "client_cert_enc", "client_key_enc"} {
		if _, ok := m[k]; ok {
			t.Errorf("peer response has secret-bearing field %q", k)
		}
	}

	if m["has_ca_pem"] != false || m["has_client_cert"] != false {
		t.Errorf("has_ca_pem/has_client_cert = %v/%v, want false/false", m["has_ca_pem"], m["has_client_cert"])
	}

	if m["name"] != "east" || m["base_url"] != fedAPIPeerURL || m["enabled"] != true {
		t.Errorf("peer = %v", m)
	}

	if e.kicks.Load() != 1 {
		t.Errorf("kicks after create = %d, want 1", e.kicks.Load())
	}

	id, _ := m["id"].(string)

	// Status lists the peer, still without secrets.
	_, st, raw := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")
	if strings.Contains(raw, secret) {
		t.Errorf("status echoes the peer token: %s", raw)
	}

	if peers, _ := st["peers"].([]any); len(peers) != 1 {
		t.Fatalf("peers = %v, want 1", st["peers"])
	}

	fedAPIUpdateAndDeletePeer(t, e, id)
}

// fedAPIUpdateAndDeletePeer renames, disables and deletes a peer, checking the
// responses never echo the submitted token.
func fedAPIUpdateAndDeletePeer(t *testing.T, e *fedAPIEnv, id string) {
	t.Helper()

	// Update: rename, disable; the token in the request is not echoed either.
	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPatch, fedAPIPath("files", "/peers/"+id),
		`{"name":"east2","enabled":false,"token":"another-secret"}`)
	if code != http.StatusOK || m["name"] != "east2" || m["enabled"] != false {
		t.Fatalf("update peer: %d %s", code, raw)
	}

	if strings.Contains(raw, "another-secret") {
		t.Errorf("update response echoes the token: %s", raw)
	}

	// Update validates like create.
	if code, _, raw := e.do(t, fedAPIAdmin, http.MethodPatch, fedAPIPath("files", "/peers/"+id),
		`{"base_url":"http://93.184.216.34"}`); code != http.StatusBadRequest {
		t.Errorf("patch with http url = %d %s, want 400", code, raw)
	}

	// Delete.
	if code, _, raw := e.do(t, fedAPIAdmin, http.MethodDelete, fedAPIPath("files", "/peers/"+id), ""); code != http.StatusNoContent {
		t.Fatalf("delete peer: %d %s", code, raw)
	}

	if code, _, _ := e.do(t, fedAPIAdmin, http.MethodDelete, fedAPIPath("files", "/peers/"+id), ""); code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", code)
	}

	_, st, _ := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")
	if peers, _ := st["peers"].([]any); len(peers) != 0 {
		t.Errorf("peers after delete = %v", peers)
	}
}

func TestFederationPeerWithTLSMaterialReportsFlagsOnly(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	// A syntactically invalid client certificate must be refused, never stored.
	code, _, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/peers"),
		fmt.Sprintf(`{"name":"mtls","base_url":%q,"token":"t","client_cert":"junk","client_key":"junk"}`, fedAPIPeerURL))
	if code != http.StatusBadRequest {
		t.Fatalf("invalid client cert = %d %s, want 400", code, raw)
	}

	if strings.Contains(raw, "junk") {
		t.Errorf("error echoes submitted key material: %s", raw)
	}
}

func TestFederationDuplicatePeerName(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	e.addPeer(t, "files", "east")
	other := e.addPeer(t, "files", "west")

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/peers"),
		fmt.Sprintf(`{"name":"east","base_url":%q,"token":"t"}`, fedAPIPeerURL))
	if code != http.StatusConflict || m["code"] != "conflict" {
		t.Fatalf("duplicate create = %d %s, want 409 conflict", code, raw)
	}

	code, m, raw = e.do(t, fedAPIAdmin, http.MethodPatch, fedAPIPath("files", "/peers/"+other), `{"name":"east"}`)
	if code != http.StatusConflict || m["code"] != "conflict" {
		t.Fatalf("duplicate rename = %d %s, want 409 conflict", code, raw)
	}
}

func TestFederationPeerIsScopedToRepository(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	fedAPISeedRepo(t, e.db, "other", "raw", "hosted")
	e.enable(t, "files")
	e.enable(t, "other")

	id := e.addPeer(t, "files", "east")

	for _, rt := range []struct{ method, tail, body string }{
		{http.MethodPatch, "/peers/" + id, `{"enabled":false}`},
		{http.MethodDelete, "/peers/" + id, ""},
		{http.MethodPost, "/peers/" + id + "/resync", ""},
		{http.MethodGet, "/peers/" + id + "/deferred", ""},
	} {
		code, _, raw := e.do(t, fedAPIAdmin, rt.method, fedAPIPath("other", rt.tail), rt.body)
		if code != http.StatusNotFound {
			t.Errorf("%s via another repository = %d %s, want 404", rt.method+rt.tail, code, raw)
		}
	}

	_, st, _ := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")
	if peers, _ := st["peers"].([]any); len(peers) != 1 {
		t.Errorf("the peer was changed through another repository: %v", st["peers"])
	}
}

func TestFederationResync(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	id := e.addPeer(t, "files", "east")
	before := e.kicks.Load()

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/peers/"+id+"/resync"), "")
	if code != http.StatusAccepted {
		t.Fatalf("resync: %d %s", code, raw)
	}

	if m["resync_requested"] != true {
		t.Errorf("resync_requested = %v, want true", m["resync_requested"])
	}

	if got := e.kicks.Load(); got != before+1 {
		t.Errorf("kicks = %d, want %d", got, before+1)
	}

	_, st, _ := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")

	peers, _ := st["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("peers = %v", st["peers"])
	}

	if p, _ := peers[0].(map[string]any); p["resync_requested"] != true {
		t.Errorf("status peer resync_requested = %v, want true", p["resync_requested"])
	}
}

func TestFederationNilKickIsSafe(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	h := NewFederationHandler(e.pg, e.log, e.eval, e.store, true, nil)
	r := chi.NewRouter()
	h.RegisterAuthed(r)
	e.router = r

	e.addPeer(t, "files", "east")
}

func TestFederationDeferredListIsEmptyArray(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	id := e.addPeer(t, "files", "east")

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files", "/peers/"+id+"/deferred"), "")
	if code != http.StatusOK {
		t.Fatalf("deferred: %d %s", code, raw)
	}

	if items, ok := m["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want empty array", m["items"])
	}
}

func TestFederationTokens(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	for _, body := range []string{`{`, `{}`, `{"name":"bad name"}`, `{"name":"-x"}`} {
		if code, _, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/tokens"), body); code != http.StatusBadRequest {
			t.Errorf("create token %s = %d %s, want 400", body, code, raw)
		}
	}

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/tokens"), `{"name":"east-pull"}`)
	if code != http.StatusCreated {
		t.Fatalf("create token: %d %s", code, raw)
	}

	secret, _ := m["token"].(string)
	id, _ := m["id"].(string)

	if secret == "" || id == "" || m["name"] != "east-pull" {
		t.Fatalf("created token = %v, want id, name and a plaintext token", m)
	}

	// The plaintext appears exactly once: never in status, never in the database.
	_, _, statusRaw := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")
	if strings.Contains(statusRaw, secret) {
		t.Errorf("status leaks the token plaintext: %s", statusRaw)
	}

	var stored int
	if err := e.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM federation_peer_tokens WHERE token_hash = $1`, secret).Scan(&stored); err != nil {
		t.Fatalf("count tokens: %v", err)
	}

	if stored != 0 {
		t.Error("token plaintext stored in the database")
	}

	_, st, _ := e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")

	tokens, _ := st["tokens"].([]any)
	if len(tokens) != 1 {
		t.Fatalf("tokens = %v, want 1", st["tokens"])
	}

	listed, _ := tokens[0].(map[string]any)
	if _, ok := listed["token"]; ok || listed["id"] != id || listed["revoked_at"] != nil {
		t.Errorf("listed token = %v", listed)
	}

	if code, _, raw := e.do(t, fedAPIAdmin, http.MethodDelete, fedAPIPath("files", "/tokens/"+id), ""); code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", code, raw)
	}

	_, st, _ = e.do(t, fedAPIAdmin, http.MethodGet, fedAPIPath("files"), "")

	tokens, _ = st["tokens"].([]any)
	if len(tokens) != 1 {
		t.Fatalf("revoked token vanished from the list: %v", st["tokens"])
	}

	if listed, _ = tokens[0].(map[string]any); listed["revoked_at"] == nil {
		t.Errorf("revoked_at not set after revoke: %v", listed)
	}
}

func TestFederationTokenScopedToRepository(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	fedAPISeedRepo(t, e.db, "other", "raw", "hosted")
	e.enable(t, "files")
	e.enable(t, "other")

	_, m, _ := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/tokens"), `{"name":"t1"}`)
	id, _ := m["id"].(string)

	if code, _, raw := e.do(t, fedAPIAdmin, http.MethodDelete, fedAPIPath("other", "/tokens/"+id), ""); code != http.StatusNotFound {
		t.Errorf("revoke through another repository = %d %s, want 404", code, raw)
	}
}

func TestFederationInternalErrorsDoNotLeak(t *testing.T) {
	e := newFedAPIEnv(t, true)
	fedAPISeedRepo(t, e.db, "files", "raw", "hosted")
	e.enable(t, "files")

	// The repository lookup still works through a second connection pool; break
	// only the store's pool so the handler reaches its database-error paths.
	broken, err := sql.Open("pgx", "postgres://nobody:pw@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = broken.Close() })

	e.store = federation.NewStore(broken)
	e.mount(true)

	for _, rt := range []struct{ method, tail, body string }{
		{http.MethodGet, "", ""},
		{http.MethodPut, "", `{"enabled":true}`},
		{http.MethodPut, "", `{"enabled":false}`},
	} {
		code, m, raw := e.do(t, fedAPIAdmin, rt.method, fedAPIPath("files", rt.tail), rt.body)
		if code != http.StatusInternalServerError || m["code"] != "internal_error" {
			t.Fatalf("%s = %d %s, want 500 internal_error", rt.method, code, raw)
		}

		for _, leak := range []string{"127.0.0.1", "nobody", "pgx", "connect", "refused", "sql", "dial"} {
			if strings.Contains(strings.ToLower(raw), leak) {
				t.Errorf("%s error body leaks %q: %s", rt.method, leak, raw)
			}
		}
	}
}
