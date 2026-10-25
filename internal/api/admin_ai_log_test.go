// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errSettingWrite = errors.New("setting write failed")

type failingSettingsDB struct{ databasetest.BaseMockDB }

func (failingSettingsDB) SetSystemSetting(context.Context, string, string) error {
	return errSettingWrite
}

// TestAdminAIConfigFailureDoesNotLogAPIKey asserts the API key submitted with a
// config update never reaches the log, even on the save-failure path.
func TestAdminAIConfigFailureDoesNotLogAPIKey(t *testing.T) {
	const secret = "sk-test-secret-value-1234"

	logPath := filepath.Join(t.TempDir(), "log.json")

	log, err := logger.New("debug", "json", logPath)
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	db := &failingSettingsDB{}

	r := chi.NewRouter()
	NewAdminAIHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/api/v1/admin/ai/config",
		strings.NewReader(`{"apiKey":"`+secret+`","model":"m"}`))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	_ = log.Sync()

	if rw.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rw.Code, rw.Body.String())
	}

	out, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(out) == 0 {
		t.Fatal("expected the failure to be logged")
	}

	if strings.Contains(string(out), secret) {
		t.Fatalf("api key leaked into log: %s", out)
	}
}
