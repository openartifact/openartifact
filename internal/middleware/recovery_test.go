// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "log.json")

	log, err := logger.New("error", "json", logPath)
	if err != nil {
		t.Fatal(err)
	}

	const secret = "db password hunter2"

	h := RequestIDMiddleware(RecoveryMiddleware(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(secret)
	})))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("panic text leaked in response: %q", rec.Body.String())
	}

	id := rec.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("request id lost after panic")
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not the shared error shape: %v", rec.Body.String(), err)
	}

	if body.Error == "" || body.Code == "" {
		t.Errorf("error envelope incomplete: %+v", body)
	}

	_ = log.Sync()

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(logged), id) {
		t.Errorf("panic log does not carry request id %s: %s", id, logged)
	}
}
