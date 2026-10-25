// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/dtrack"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/pkg/logger"
)

const dtSecretKey = "odt_super_secret_value"

type dtDB struct {
	databasetest.BaseMockDB

	mu       sync.Mutex
	settings map[string]string
	row      *models.RepositoryDependencyTrack
	pushes   map[string]*models.DependencyTrackPush
}

func (d *dtDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if name != "libs" {
		return nil, nil
	}

	return &models.Repository{ID: attestRepoID, Name: "libs", Format: "npm"}, nil
}

func (d *dtDB) GetSystemSetting(_ context.Context, k string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.settings[k], nil
}

func (d *dtDB) SetSystemSetting(_ context.Context, k, v string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.settings[k] = v

	return nil
}

func (d *dtDB) GetRepositoryDependencyTrack(context.Context, string) (*models.RepositoryDependencyTrack, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.row == nil {
		return nil, nil
	}

	c := *d.row

	return &c, nil
}

func (d *dtDB) UpsertRepositoryDependencyTrack(_ context.Context, r *models.RepositoryDependencyTrack) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	c := *r
	d.row = &c

	return nil
}

func (d *dtDB) DeleteRepositoryDependencyTrack(context.Context, string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	had := d.row != nil
	d.row = nil

	return had, nil
}

func (d *dtDB) GetDependencyTrackPush(_ context.Context, _, id string) (*models.DependencyTrackPush, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.pushes[id], nil
}

func (d *dtDB) UpsertDependencyTrackPush(_ context.Context, p *models.DependencyTrackPush) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	c := *p
	d.pushes[p.ArtifactID] = &c

	return nil
}

func (d *dtDB) ListDependencyTrackPushes(context.Context, string, int) ([]*models.DependencyTrackPush, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.DependencyTrackPush
	for _, p := range d.pushes {
		out = append(out, p)
	}

	return out, nil
}

type fakePusher struct{ jobs []dtrack.Job }

func (f *fakePusher) Enqueue(j dtrack.Job) bool {
	f.jobs = append(f.jobs, j)
	return true
}

type dtFixture struct {
	router chi.Router
	db     *dtDB
	pusher *fakePusher
	dt     *httptest.Server
}

// newDTFixture serves a fake Dependency-Track and routes the handler's clients to it
// (the production client refuses loopback addresses).
func newDTFixture(t *testing.T) *dtFixture {
	t.Helper()

	mustNoError(t, crypto.Init("0123456789abcdef0123456789abcdef0123456789"))
	t.Cleanup(func() { _ = crypto.Init("") })

	// The fake server listens on loopback; cloud metadata addresses stay blocked.
	t.Setenv(netutil.EnvAllowPrivate, "true")

	dt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != dtSecretKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"4.14.2"}`))
		case "/api/v1/team/self":
			_, _ = w.Write([]byte(`{"permissions":[{"name":"BOM_UPLOAD"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(dt.Close)

	log, err := logger.New("error", "console", "stderr")
	mustNoError(t, err)

	db := &dtDB{settings: map[string]string{}, pushes: map[string]*models.DependencyTrackPush{}}
	pusher := &fakePusher{}

	r := chi.NewRouter()
	NewDependencyTrackHandler(db, log, auth.NewEvaluator(db, log, false), pusher).RegisterAuthed(r)

	return &dtFixture{router: r, db: db, pusher: pusher, dt: dt}
}

func (f *dtFixture) do(t *testing.T, method, target, body string, admin bool) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if admin {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))
	}

	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)

	return rw
}

const (
	dtGlobal = "/api/v1/admin/dependency-track"
	dtRepo   = "/api/v1/repositories/libs/dependency-track"
)

func TestDependencyTrackGlobalSettingsNeverReturnTheKey(t *testing.T) {
	f := newDTFixture(t)

	rw := f.do(t, http.MethodGet, dtGlobal, "", true)
	mustEqual(t, http.StatusOK, rw.Code)
	expectJSONEq(t, `{"enabled":false,"base_url":"","api_key_set":false,"project_name":"","project_version":"","auto_push":false,"push_vex":false}`, rw.Body.String())

	body := `{"enabled":true,"base_url":"` + f.dt.URL + `","api_key":"` + dtSecretKey + `","project_name":"{repo}/{name}","auto_push":true,"push_vex":true}`
	rw = f.do(t, http.MethodPut, dtGlobal, body, true)
	mustEqual(t, http.StatusOK, rw.Code, rw.Body.String())
	expectNotContains(t, rw.Body.String(), dtSecretKey)
	expectContains(t, rw.Body.String(), `"api_key_set":true`)
	expectNotContains(t, f.db.settings[dtrack.SettingGlobal], dtSecretKey, "stored encrypted")

	// Omitting api_key keeps the stored one.
	rw = f.do(t, http.MethodPut, dtGlobal, `{"enabled":true,"base_url":"`+f.dt.URL+`","auto_push":false,"push_vex":true}`, true)
	mustEqual(t, http.StatusOK, rw.Code, rw.Body.String())
	expectContains(t, rw.Body.String(), `"api_key_set":true`)

	rw = f.do(t, http.MethodGet, dtGlobal, "", true)
	expectNotContains(t, rw.Body.String(), dtSecretKey)

	// An empty api_key clears it, which makes enabling invalid.
	rw = f.do(t, http.MethodPut, dtGlobal, `{"enabled":true,"base_url":"`+f.dt.URL+`","api_key":""}`, true)
	expectEqual(t, http.StatusBadRequest, rw.Code)

	rw = f.do(t, http.MethodPut, dtGlobal, `{"enabled":false,"base_url":"`+f.dt.URL+`","api_key":""}`, true)
	mustEqual(t, http.StatusOK, rw.Code)
	expectContains(t, rw.Body.String(), `"api_key_set":false`)
}

func TestDependencyTrackGlobalSettingsValidation(t *testing.T) {
	f := newDTFixture(t)

	for name, body := range map[string]string{
		"invalid json":         `{`,
		"private metadata url": `{"enabled":false,"base_url":"http://169.254.169.254/"}`,
		"not http":             `{"enabled":false,"base_url":"file:///etc/passwd"}`,
		"unknown placeholder":  `{"enabled":false,"project_name":"{nope}"}`,
		"enable needs url/key": `{"enabled":true}`,
		"encrypted key given":  `{"enabled":false,"api_key":"enc:v1:abcdef"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rw := f.do(t, http.MethodPut, dtGlobal, body, true)
			expectEqual(t, http.StatusBadRequest, rw.Code, rw.Body.String())
			expectNotContains(t, rw.Body.String(), "169.254", "errors do not echo internals")
		})
	}
}

func TestDependencyTrackEndpointsAreAdminOnly(t *testing.T) {
	f := newDTFixture(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, dtGlobal}, {http.MethodPut, dtGlobal}, {http.MethodPost, dtGlobal + "/test"},
		{http.MethodGet, dtRepo}, {http.MethodPut, dtRepo}, {http.MethodDelete, dtRepo},
		{http.MethodPost, dtRepo + "/test"}, {http.MethodPost, dtRepo + "/push"}, {http.MethodGet, dtRepo + "/pushes"},
	} {
		rw := f.do(t, tc.method, tc.path, `{}`, false)
		expectEqual(t, http.StatusForbidden, rw.Code, tc.method+" "+tc.path)
	}
}

func TestDependencyTrackConnectionTest(t *testing.T) {
	f := newDTFixture(t)

	// Not configured.
	expectEqual(t, http.StatusBadRequest, f.do(t, http.MethodPost, dtGlobal+"/test", "", true).Code)

	f.do(t, http.MethodPut, dtGlobal, `{"enabled":true,"base_url":"`+f.dt.URL+`","api_key":"`+dtSecretKey+`","push_vex":true}`, true)

	rw := f.do(t, http.MethodPost, dtGlobal+"/test", "", true)
	mustEqual(t, http.StatusOK, rw.Code, rw.Body.String())

	var out struct {
		Version string   `json:"version"`
		Missing []string `json:"missing_permissions"`
	}

	mustNoError(t, json.Unmarshal(rw.Body.Bytes(), &out))
	expectEqual(t, "4.14.2", out.Version)
	expectContains(t, out.Missing, "VULNERABILITY_ANALYSIS")

	// A wrong key fails with a generic message.
	f.do(t, http.MethodPut, dtGlobal, `{"enabled":true,"base_url":"`+f.dt.URL+`","api_key":"wrong-key-value"}`, true)

	rw = f.do(t, http.MethodPost, dtGlobal+"/test", "", true)
	expectEqual(t, http.StatusBadRequest, rw.Code)
	expectNotContains(t, rw.Body.String(), f.dt.URL, "the upstream address is not echoed")
	expectNotContains(t, rw.Body.String(), "wrong-key-value")
}

func TestDependencyTrackRepositoryOverride(t *testing.T) {
	f := newDTFixture(t)

	f.do(t, http.MethodPut, dtGlobal, `{"enabled":true,"base_url":"`+f.dt.URL+`","api_key":"`+dtSecretKey+`","auto_push":true,"push_vex":true}`, true)

	rw := f.do(t, http.MethodGet, dtRepo, "", true)
	mustEqual(t, http.StatusOK, rw.Code)

	var got struct {
		Override  *json.RawMessage `json:"override"`
		Effective struct {
			Enabled     bool   `json:"enabled"`
			Ready       bool   `json:"ready"`
			Source      string `json:"source"`
			ProjectName string `json:"project_name"`
		} `json:"effective"`
	}

	mustNoError(t, json.Unmarshal(rw.Body.Bytes(), &got))
	expectNil(t, got.Override)
	expectTrue(t, got.Effective.Ready)
	expectEqual(t, "global", got.Effective.Source)
	expectEqual(t, "{repo}/{name}", got.Effective.ProjectName, "defaults are shown")

	// A per-repo override with its own key, opting out of VEX.
	rw = f.do(t, http.MethodPut, dtRepo, `{"enabled":true,"project_name":"x/{name}","api_key":"repo-key-value","push_vex":false}`, true)
	mustEqual(t, http.StatusOK, rw.Code, rw.Body.String())
	expectNotContains(t, rw.Body.String(), "repo-key-value")
	expectNotContains(t, f.db.row.APIKey, "repo-key-value", "stored encrypted")

	mustNoError(t, json.Unmarshal(rw.Body.Bytes(), &got))
	expectEqual(t, "repository", got.Effective.Source)
	expectEqual(t, "x/{name}", got.Effective.ProjectName)

	// Unknown repository, and removal.
	expectEqual(t, http.StatusNotFound, f.do(t, http.MethodGet, "/api/v1/repositories/nope/dependency-track", "", true).Code)
	expectEqual(t, http.StatusNoContent, f.do(t, http.MethodDelete, dtRepo, "", true).Code)
	expectEqual(t, http.StatusNotFound, f.do(t, http.MethodDelete, dtRepo, "", true).Code)
}

func TestDependencyTrackManualPush(t *testing.T) {
	f := newDTFixture(t)

	// Nothing configured: refused.
	rw := f.do(t, http.MethodPost, dtRepo+"/push", `{"artifact_id":"lib@1.0.0"}`, true)
	expectEqual(t, http.StatusConflict, rw.Code)
	expectEmpty(t, f.pusher.jobs)

	f.do(t, http.MethodPut, dtGlobal, `{"enabled":true,"base_url":"`+f.dt.URL+`","api_key":"`+dtSecretKey+`"}`, true)

	expectEqual(t, http.StatusBadRequest, f.do(t, http.MethodPost, dtRepo+"/push", `{"artifact_id":""}`, true).Code)

	rw = f.do(t, http.MethodPost, dtRepo+"/push", `{"artifact_id":"lib@1.0.0"}`, true)
	mustEqual(t, http.StatusAccepted, rw.Code, rw.Body.String())
	mustLen(t, f.pusher.jobs, 1)
	expectEqual(t, dtrack.Job{RepoID: attestRepoID, ArtifactID: "lib@1.0.0", Trigger: dtrack.TriggerManual}, f.pusher.jobs[0])
	expectContains(t, rw.Body.String(), `"state":"queued"`)

	rw = f.do(t, http.MethodGet, dtRepo+"/pushes", "", true)
	mustEqual(t, http.StatusOK, rw.Code)
	expectContains(t, rw.Body.String(), `"artifact_id":"lib@1.0.0"`)
}

func TestDependencyTrackPushWithoutPusherIsNotImplemented(t *testing.T) {
	f := newDTFixture(t)
	log, err := logger.New("error", "console", "stderr")
	mustNoError(t, err)

	r := chi.NewRouter()
	NewDependencyTrackHandler(f.db, log, auth.NewEvaluator(f.db, log, false), nil).RegisterAuthed(r)
	f.router = r

	rw := f.do(t, http.MethodPost, dtRepo+"/push", `{"artifact_id":"lib@1.0.0"}`, true)
	expectEqual(t, http.StatusNotImplemented, rw.Code)
}
