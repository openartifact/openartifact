// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	vexFeedID   = "7e000000-0000-4000-8000-000000000001"
	vexSuggID   = "7e000000-0000-4000-8000-000000000002"
	vexRepoID   = "7e000000-0000-4000-8000-0000000000a1"
	vexOtherID  = "7e000000-0000-4000-8000-0000000000b1"
	vexFeedURL  = "https://93.184.216.34/.well-known/csaf/provider-metadata.json"
	vexPeerName = "upstream"
)

// vexMockDB serves the feed and suggestion queries and the authorization reads.
type vexMockDB struct {
	auth.EvaluatorStore

	feeds       map[string]*models.VEXFeed
	suggestions map[string]*models.VEXSuggestion // by id; RepositoryID scopes them
	statements  []*models.VEXStatement
	repos       map[string]*models.Repository
}

func newVEXMockDB() *vexMockDB {
	return &vexMockDB{
		feeds: map[string]*models.VEXFeed{},
		suggestions: map[string]*models.VEXSuggestion{vexSuggID: {
			ID: vexSuggID, RepositoryID: vexRepoID, PeerID: "p1", PeerName: vexPeerName, PeerBaseURL: "https://peer.example",
			ArtifactID: "app@1.0", VulnID: "CVE-2024-1", Status: models.VEXNotAffected,
			Justification: models.VEXComponentNotPresent, State: models.VEXSuggestionPending,
		}},
		repos: map[string]*models.Repository{
			"libs":  {ID: vexRepoID, Name: "libs", Format: "npm"},
			"other": {ID: vexOtherID, Name: "other", Format: "npm"},
		},
	}
}

func (db *vexMockDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	if userID == uAdmin1 {
		return adminTargets(), nil
	}

	return nil, nil
}

func (db *vexMockDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

func (db *vexMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return db.repos[name], nil
}

func (db *vexMockDB) ListVEXFeeds(context.Context) ([]*models.VEXFeed, error) {
	out := make([]*models.VEXFeed, 0, len(db.feeds))
	for _, f := range db.feeds {
		out = append(out, f)
	}

	return out, nil
}

func (db *vexMockDB) GetVEXFeed(_ context.Context, id string) (*models.VEXFeed, error) {
	if f, ok := db.feeds[id]; ok {
		c := *f
		return &c, nil
	}

	return nil, database.ErrVEXFeedNotFound
}

func (db *vexMockDB) CreateVEXFeed(_ context.Context, f *models.VEXFeed) error {
	for _, e := range db.feeds {
		if e.Name == f.Name {
			return database.ErrVEXFeedConflict
		}
	}

	f.ID = vexFeedID
	c := *f
	db.feeds[f.ID] = &c

	return nil
}

func (db *vexMockDB) UpdateVEXFeed(_ context.Context, f *models.VEXFeed) error {
	c := *f
	db.feeds[f.ID] = &c

	return nil
}

func (db *vexMockDB) DeleteVEXFeed(_ context.Context, id string) error {
	delete(db.feeds, id)
	return nil
}

func (db *vexMockDB) suggestion(repoID, id string) (*models.VEXSuggestion, error) {
	s, ok := db.suggestions[id]
	if !ok || s.RepositoryID != repoID {
		return nil, database.ErrVEXSuggestionNotFound
	}

	return s, nil
}

func (db *vexMockDB) ListVEXSuggestions(_ context.Context, repoID, state string) ([]*models.VEXSuggestion, error) {
	var out []*models.VEXSuggestion

	for _, s := range db.suggestions {
		if s.RepositoryID == repoID && (state == "" || s.State == state) {
			out = append(out, s)
		}
	}

	return out, nil
}

func (db *vexMockDB) GetVEXSuggestion(_ context.Context, repoID, id string) (*models.VEXSuggestion, error) {
	return db.suggestion(repoID, id)
}

func (db *vexMockDB) SetVEXSuggestionState(_ context.Context, repoID, id, state, decidedBy string) error {
	s, err := db.suggestion(repoID, id)
	if err != nil {
		return err
	}

	s.State, s.DecidedBy = state, decidedBy

	return nil
}

func (db *vexMockDB) AcceptVEXSuggestion(_ context.Context, repoID, id, decidedBy string) (*models.VEXStatement, error) {
	s, err := db.suggestion(repoID, id)
	if err != nil {
		return nil, err
	}

	stmt := &models.VEXStatement{
		ID: "7e000000-0000-4000-8000-0000000000c1", RepositoryID: repoID, ArtifactID: s.ArtifactID, VulnID: s.VulnID,
		Status: s.Status, Justification: s.Justification, CreatedBy: decidedBy,
		SourceKind: models.VEXSourcePeer, SourceName: s.PeerName, SourceRef: s.PeerBaseURL, PeerID: s.PeerID,
	}
	db.statements = append(db.statements, stmt)
	s.State, s.DecidedBy = models.VEXSuggestionAccepted, decidedBy

	return stmt, nil
}

// fakeSyncer starts a sync unless one of the feed is running.
type fakeSyncer struct{ running map[string]bool }

func (s *fakeSyncer) StartSync(id string) bool {
	if s.running[id] {
		return false
	}

	s.running[id] = true

	return true
}

//nolint:gochecknoglobals // immutable principals shared by the tests
var (
	vexAdmin = &auth.Principal{UserID: new(uAdmin1), Username: "admin-1"}
	vexPlain = &auth.Principal{UserID: new(uPlain1), Username: "plain-1"}
)

func serveVEX(t *testing.T, register func(chi.Router), p *auth.Principal, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), p)))
		})
	})
	register(r)

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	return rw
}

func vexTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return log
}

func newFeedHandler(t *testing.T, db *vexMockDB, syncer VEXFeedSyncer) func(chi.Router) {
	t.Helper()

	log := vexTestLogger(t)

	return NewVEXFeedHandler(db, log, auth.NewEvaluator(db, log, false), syncer).RegisterAuthed
}

func armoredTestKey(t *testing.T) string {
	t.Helper()

	e, err := openpgp.NewEntity("vendor", "", "psirt@vendor.example", nil)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

func feedBody(t *testing.T, fields map[string]any) string {
	t.Helper()

	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestVEXFeeds_CreateValidation(t *testing.T) {
	key := armoredTestKey(t)

	for _, tc := range []struct {
		name   string
		fields map[string]any
		want   int
	}{
		{"valid with key", map[string]any{"name": "vendor", "url": vexFeedURL, "pgp_keys": key, "interval_hours": 6}, http.StatusCreated},
		{"defaults", map[string]any{"name": "vendor", "url": vexFeedURL}, http.StatusCreated},
		{"plain http", map[string]any{"name": "vendor", "url": "http://93.184.216.34/x.json"}, http.StatusBadRequest},
		{"not a url", map[string]any{"name": "vendor", "url": "vendor"}, http.StatusBadRequest},
		{"private address", map[string]any{"name": "vendor", "url": "https://10.0.0.1/x.json"}, http.StatusBadRequest},
		{"bad pgp", map[string]any{"name": "vendor", "url": vexFeedURL, "pgp_keys": "not a key"}, http.StatusBadRequest},
		{"interval zero", map[string]any{"name": "vendor", "url": vexFeedURL, "interval_hours": 0}, http.StatusBadRequest},
		{"interval too long", map[string]any{"name": "vendor", "url": vexFeedURL, "interval_hours": 721}, http.StatusBadRequest},
		{"bad name", map[string]any{"name": "-x", "url": vexFeedURL}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newVEXMockDB()
			rw := serveVEX(t, newFeedHandler(t, db, nil), vexAdmin, http.MethodPost, "/api/v1/vex/feeds", feedBody(t, tc.fields))

			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rw.Code, tc.want, rw.Body)
			}

			if tc.want != http.StatusCreated {
				if len(db.feeds) != 0 {
					t.Error("an invalid feed was stored")
				}

				return
			}

			f := db.feeds[vexFeedID]
			if f == nil || !f.Enabled || !f.RequireSignature || f.IntervalHours == 0 {
				t.Fatalf("stored feed = %+v, want enabled, signature required, an interval", f)
			}

			if _, ok := tc.fields["interval_hours"]; !ok && f.IntervalHours != 24 {
				t.Errorf("default interval = %d, want 24", f.IntervalHours)
			}
		})
	}
}

func TestVEXFeeds_AdminOnly(t *testing.T) {
	db := newVEXMockDB()
	db.feeds[vexFeedID] = &models.VEXFeed{ID: vexFeedID, Name: "vendor", URL: vexFeedURL}
	syncer := &fakeSyncer{running: map[string]bool{}}

	for _, rt := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/vex/feeds", ""},
		{http.MethodPost, "/api/v1/vex/feeds", `{"name":"x","url":"` + vexFeedURL + `"}`},
		{http.MethodGet, "/api/v1/vex/feeds/" + vexFeedID, ""},
		{http.MethodPut, "/api/v1/vex/feeds/" + vexFeedID, `{"name":"x","url":"` + vexFeedURL + `"}`},
		{http.MethodDelete, "/api/v1/vex/feeds/" + vexFeedID, ""},
		{http.MethodPost, "/api/v1/vex/feeds/" + vexFeedID + "/sync", ""},
	} {
		if rw := serveVEX(t, newFeedHandler(t, db, syncer), vexPlain, rt.method, rt.target, rt.body); rw.Code != http.StatusForbidden {
			t.Errorf("%s %s by a non-admin = %d, want 403", rt.method, rt.target, rw.Code)
		}
	}

	if db.feeds[vexFeedID].Name != "vendor" || len(syncer.running) != 0 {
		t.Error("a non-admin request changed state")
	}
}

func TestVEXFeeds_Sync(t *testing.T) {
	db := newVEXMockDB()
	db.feeds[vexFeedID] = &models.VEXFeed{ID: vexFeedID, Name: "vendor", URL: vexFeedURL}
	h := newFeedHandler(t, db, &fakeSyncer{running: map[string]bool{}})
	target := "/api/v1/vex/feeds/" + vexFeedID + "/sync"

	if rw := serveVEX(t, h, vexAdmin, http.MethodPost, target, ""); rw.Code != http.StatusAccepted ||
		!strings.Contains(rw.Body.String(), `"status":"started"`) {
		t.Fatalf("first sync = %d %s, want 202 started", rw.Code, rw.Body)
	}

	if rw := serveVEX(t, h, vexAdmin, http.MethodPost, target, ""); rw.Code != http.StatusConflict {
		t.Fatalf("sync while running = %d, want 409", rw.Code)
	}

	missing := "/api/v1/vex/feeds/7e000000-0000-4000-8000-0000000000ff/sync"
	if rw := serveVEX(t, h, vexAdmin, http.MethodPost, missing, ""); rw.Code != http.StatusNotFound {
		t.Fatalf("sync of an unknown feed = %d, want 404", rw.Code)
	}

	if rw := serveVEX(t, newFeedHandler(t, db, nil), vexAdmin, http.MethodPost, target, ""); rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("sync without a syncer = %d, want 503", rw.Code)
	}
}

func newSuggestionHandler(t *testing.T, db *vexMockDB) func(chi.Router) {
	t.Helper()

	log := vexTestLogger(t)

	return NewVEXSuggestionHandler(db, log, auth.NewEvaluator(db, log, false)).RegisterAuthed
}

func TestVEXSuggestions_Accept(t *testing.T) {
	db := newVEXMockDB()
	h := newSuggestionHandler(t, db)

	// The suggestion belongs to libs: through another repository it does not exist.
	if rw := serveVEX(t, h, vexAdmin, http.MethodPost, "/api/v1/repositories/other/vex/suggestions/"+vexSuggID+"/accept", ""); rw.Code !=
		http.StatusNotFound {
		t.Fatalf("accept through another repository = %d, want 404", rw.Code)
	}

	if rw := serveVEX(t, h, vexPlain, http.MethodPost, "/api/v1/repositories/libs/vex/suggestions/"+vexSuggID+"/accept", ""); rw.Code !=
		http.StatusForbidden {
		t.Fatalf("accept by a non-admin = %d, want 403", rw.Code)
	}

	if len(db.statements) != 0 {
		t.Fatal("a rejected accept wrote a statement")
	}

	rw := serveVEX(t, h, vexAdmin, http.MethodPost, "/api/v1/repositories/libs/vex/suggestions/"+vexSuggID+"/accept", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rw.Code, rw.Body)
	}

	var got struct {
		SourceKind string `json:"source_kind"`
		SourceName string `json:"source_name"`
		SourceRef  string `json:"source_ref"`
		CreatedBy  string `json:"created_by"`
		VulnID     string `json:"vuln_id"`
	}
	decodeBody(t, rw, &got)

	if got.SourceKind != models.VEXSourcePeer || got.SourceName != vexPeerName || got.SourceRef != "https://peer.example" ||
		got.CreatedBy != "admin-1" || got.VulnID != "CVE-2024-1" {
		t.Errorf("accepted statement = %+v", got)
	}

	if len(db.statements) != 1 || db.statements[0].RepositoryID != vexRepoID || db.statements[0].PeerID != "p1" {
		t.Errorf("statements = %+v, want one of libs from peer p1", db.statements)
	}
}

func TestVEXSuggestions_ListAndDismiss(t *testing.T) {
	db := newVEXMockDB()
	h := newSuggestionHandler(t, db)

	var list struct {
		Suggestions []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"suggestions"`
	}

	decodeBody(t, serveVEX(t, h, vexAdmin, http.MethodGet, "/api/v1/repositories/libs/vex/suggestions", ""), &list)

	if len(list.Suggestions) != 1 || list.Suggestions[0].ID != vexSuggID {
		t.Fatalf("pending suggestions = %+v", list.Suggestions)
	}

	if rw := serveVEX(t, h, vexAdmin, http.MethodGet, "/api/v1/repositories/libs/vex/suggestions?state=bogus", ""); rw.Code !=
		http.StatusBadRequest {
		t.Fatalf("bad state = %d, want 400", rw.Code)
	}

	if rw := serveVEX(t, h, vexAdmin, http.MethodPost, "/api/v1/repositories/other/vex/suggestions/"+vexSuggID+"/dismiss", ""); rw.Code !=
		http.StatusNotFound {
		t.Fatalf("dismiss through another repository = %d, want 404", rw.Code)
	}

	rw := serveVEX(t, h, vexAdmin, http.MethodPost, "/api/v1/repositories/libs/vex/suggestions/"+vexSuggID+"/dismiss", "")
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), `"state":"dismissed"`) {
		t.Fatalf("dismiss = %d %s", rw.Code, rw.Body)
	}

	list.Suggestions = nil
	decodeBody(t, serveVEX(t, h, vexAdmin, http.MethodGet, "/api/v1/repositories/libs/vex/suggestions", ""), &list)

	if len(list.Suggestions) != 0 {
		t.Errorf("pending after dismiss = %+v, want none", list.Suggestions)
	}

	list.Suggestions = nil
	decodeBody(t, serveVEX(t, h, vexAdmin, http.MethodGet, "/api/v1/repositories/libs/vex/suggestions?state=all", ""), &list)

	if len(list.Suggestions) != 1 || list.Suggestions[0].State != models.VEXSuggestionDismissed {
		t.Errorf("all suggestions = %+v, want the dismissed one", list.Suggestions)
	}
}
