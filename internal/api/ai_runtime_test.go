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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

type aiRuntimeDB struct {
	databasetest.BaseMockDB

	mu       sync.Mutex
	settings map[string]string
	blobs    map[string]*models.ComponentBlob
	saved    map[string]*models.AIAnalysis
	updates  chan struct{}
}

func newAIRuntimeDB() *aiRuntimeDB {
	return &aiRuntimeDB{
		settings: map[string]string{},
		blobs:    map[string]*models.ComponentBlob{},
		saved:    map[string]*models.AIAnalysis{},
		updates:  make(chan struct{}, 1000),
	}
}

func (d *aiRuntimeDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.settings[key], nil
}

func (d *aiRuntimeDB) SetSystemSetting(_ context.Context, key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.settings[key] = value

	return nil
}

func (d *aiRuntimeDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: "repo-1", Name: name}, nil
}

func (d *aiRuntimeDB) GetRepoComponentBlob(_ context.Context, _, id string) (*models.ComponentBlob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.blobs[id], nil
}

func (d *aiRuntimeDB) UpsertAIAnalysis(_ context.Context, a *models.AIAnalysis) error {
	d.mu.Lock()
	cp := *a
	d.saved[a.ArtifactID] = &cp
	d.mu.Unlock()

	d.updates <- struct{}{}

	return nil
}

func (d *aiRuntimeDB) awaitCompleted(t *testing.T, id string) *models.AIAnalysis {
	t.Helper()

	deadline := time.After(10 * time.Second)

	for {
		d.mu.Lock()
		a := d.saved[id]
		done := a != nil && a.Status == "completed"
		d.mu.Unlock()

		if done {
			return a
		}

		select {
		case <-d.updates:
		case <-deadline:
			t.Fatalf("timed out waiting for analysis %s", id)
		}
	}
}

func chatEndpoint(t *testing.T) *httptest.Server {
	t.Helper()

	t.Setenv(netutil.EnvAllowPrivate, "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}

		_ = json.NewDecoder(r.Body).Decode(&req)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": req.Model,
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"artifact_summary":"ok","license_status":"SAFE"}`,
			}}},
		})
	}))
	t.Cleanup(srv.Close)

	return srv
}

type aiRuntimeEnv struct {
	router chi.Router
	db     *aiRuntimeDB
	worker *worker.AIAnalysisWorker
}

func newAIRuntimeEnv(t *testing.T) *aiRuntimeEnv {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := newAIRuntimeDB()
	w := worker.NewAIAnalysisWorker(db, nil, log).WithDebounce(time.Millisecond)
	w.Start()
	t.Cleanup(w.Stop)

	r := chi.NewRouter()
	NewAdminAIHandler(db, log, auth.NewEvaluator(db, log, false)).WithRuntime(w, ai.Settings{}).Register(r)
	NewAIHandler(db, log, auth.NewEvaluator(db, log, false), w).RegisterRoutes(r, "/api/v1/repositories/{name}")

	return &aiRuntimeEnv{router: r, db: db, worker: w}
}

func (e *aiRuntimeEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	rw := httptest.NewRecorder()
	e.router.ServeHTTP(rw, req)

	return rw
}

func (e *aiRuntimeEnv) put(t *testing.T, body string) {
	t.Helper()

	if rw := e.do(http.MethodPut, "/api/v1/admin/ai/config", body); rw.Code != http.StatusOK {
		t.Fatalf("PUT %s: %d %s", body, rw.Code, rw.Body.String())
	}
}

func (e *aiRuntimeEnv) trigger(id, body string) *httptest.ResponseRecorder {
	return e.do(http.MethodPost, "/api/v1/repositories/r/ai-analysis/"+id+"/trigger", body)
}

func TestAdminAIConfigAppliesToRunningWorker(t *testing.T) {
	srv := chatEndpoint(t)
	e := newAIRuntimeEnv(t)

	if e.worker.Enabled() {
		t.Fatal("worker enabled before any configuration")
	}

	if rw := e.trigger("a", `{"manifest_text":"m"}`); rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("trigger while disabled: %d", rw.Code)
	}

	e.put(t, `{"enabled":true,"provider":"custom","base_url":"`+srv.URL+`","model":"model-1","auto_analyze":true}`)

	if !e.worker.Enabled() || !e.worker.AutoAnalyze() {
		t.Fatalf("PUT not applied: enabled=%v auto=%v", e.worker.Enabled(), e.worker.AutoAnalyze())
	}

	if rw := e.trigger("a", `{"manifest_text":"m","artifact_format":"npm"}`); rw.Code != http.StatusAccepted {
		t.Fatalf("trigger: %d %s", rw.Code, rw.Body.String())
	}

	if got := e.db.awaitCompleted(t, "a"); got.ModelUsed != "model-1" {
		t.Errorf("model_used = %q", got.ModelUsed)
	}

	// Change only the model: the next analysis uses it, no restart.
	e.put(t, `{"model":"model-2","auto_analyze":false}`)

	if e.worker.AutoAnalyze() {
		t.Error("auto_analyze=false not applied")
	}

	if rw := e.trigger("b", `{"manifest_text":"m","artifact_format":"npm"}`); rw.Code != http.StatusAccepted {
		t.Fatalf("trigger: %d", rw.Code)
	}

	if got := e.db.awaitCompleted(t, "b"); got.ModelUsed != "model-2" {
		t.Errorf("model_used = %q after changing the model", got.ModelUsed)
	}

	rw := e.do(http.MethodGet, "/api/v1/admin/ai/config", "")

	var cfg map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &cfg); err != nil || cfg["model"] != "model-2" || cfg["auto_analyze"] != false || cfg["provider"] != "custom" {
		t.Errorf("config = %v (%v)", cfg, err)
	}

	e.put(t, `{"enabled":false}`)

	if e.worker.Enabled() {
		t.Error("disabling did not stop the worker accepting work")
	}

	if rw := e.trigger("c", `{"manifest_text":"m"}`); rw.Code != http.StatusServiceUnavailable {
		t.Errorf("trigger after disable: %d", rw.Code)
	}
}

func TestAdminAIConfigValidation(t *testing.T) {
	e := newAIRuntimeEnv(t)

	tests := []struct {
		name string
		body string
		want string
	}{
		{"unknown provider names the valid ones", `{"provider":"vertex"}`, "openai, azure, ollama, custom"},
		{"custom needs an endpoint", `{"provider":"custom"}`, "base_url is required"},
		{"custom cannot clear its endpoint", `{"provider":"custom","base_url":""}`, "base_url is required"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rw := e.do(http.MethodPut, "/api/v1/admin/ai/config", tc.body)
			if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), tc.want) {
				t.Errorf("%d %s", rw.Code, rw.Body.String())
			}
		})
	}
}

func TestAITriggerWithoutManifestText(t *testing.T) {
	srv := chatEndpoint(t)
	e := newAIRuntimeEnv(t)
	e.db.blobs["npm-1"] = &models.ComponentBlob{ID: "npm-1", Format: "npm", Filename: "a-1.tgz", Name: "a", Version: "1", SHA256: "s"}
	e.db.blobs["docker-1"] = &models.ComponentBlob{ID: "docker-1", Format: "docker", Filename: "sha256:x", Name: "i", Version: "1", SHA256: "d"}
	e.put(t, `{"enabled":true,"provider":"custom","base_url":"`+srv.URL+`","model":"m"}`)

	tests := []struct {
		name string
		id   string
		body string
		want int
	}{
		{"unknown artifact", "nope", `{}`, http.StatusNotFound},
		{"artifact without a readable manifest", "docker-1", `{}`, http.StatusUnprocessableEntity},
		{"supported artifact", "npm-1", `{}`, http.StatusAccepted},
		{"caller text needs no stored artifact", "nope", `{"manifest_text":"x","artifact_format":"npm"}`, http.StatusAccepted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rw := e.trigger(tc.id, tc.body); rw.Code != tc.want {
				t.Errorf("status = %d (%s), want %d", rw.Code, rw.Body.String(), tc.want)
			}
		})
	}
}

// The admin GET shows what the worker runs with: AI enabled by the environment
// (no saved ai.enabled) is enabled in the response, and a saved value wins.
func TestAdminAIConfigGetReflectsEffectiveConfig(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	env := ai.Settings{Enabled: true, AutoAnalyze: true, Config: ai.Config{Provider: "ollama", BaseURL: "http://ollama.local:11434", Model: "env-model"}}
	db := newAIRuntimeDB()

	r := chi.NewRouter()
	NewAdminAIHandler(db, log, auth.NewEvaluator(db, log, false)).WithRuntime(nil, env).Register(r)

	get := func() map[string]any {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/admin/ai/config", nil)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

		rw := httptest.NewRecorder()
		r.ServeHTTP(rw, req)

		var cfg map[string]any
		if err := json.Unmarshal(rw.Body.Bytes(), &cfg); err != nil || rw.Code != http.StatusOK {
			t.Fatalf("GET: %d %s (%v)", rw.Code, rw.Body.String(), err)
		}

		return cfg
	}

	cfg := get()
	if cfg["enabled"] != true || cfg["auto_analyze"] != true || cfg["provider"] != "ollama" || cfg["model"] != "env-model" {
		t.Errorf("env-configured AI reported as %v", cfg)
	}

	db.settings[ai.SettingEnabled] = "false"

	if cfg := get(); cfg["enabled"] != false {
		t.Errorf("saved ai.enabled=false reported as %v", cfg["enabled"])
	}
}

func TestValidateAIArtifactIdentity(t *testing.T) {
	long := strings.Repeat("x", aiMaxNameLen+1)

	tests := []struct {
		name, format, artName, version string
		wantErr                        bool
	}{
		{"empty is fine", "", "", "", false},
		{"go is the repository format id", "go", "example.com/m", "v1.0.0", false},
		{"gomod is not a format id", "gomod", "", "", true},
		{"unknown format", "rpmx", "", "", true},
		{"name too long", "npm", long, "", true},
		{"version too long", "npm", "", long, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidateAIArtifactIdentity(tc.format, tc.artName, tc.version); (got != "") != tc.wantErr {
				t.Errorf("ValidateAIArtifactIdentity = %q, wantErr %v", got, tc.wantErr)
			}
		})
	}
}
