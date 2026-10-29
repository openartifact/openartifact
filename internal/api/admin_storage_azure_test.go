// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/pkg/logger"
)

// TestValidateStorageConfigAzure: the account becomes the host label of
// https://<account>.blob.core.windows.net, so only a real account name is
// accepted, and a custom endpoint goes through the outbound-URL check.
func TestValidateStorageConfigAzure(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "false")

	for _, tc := range []struct {
		name    string
		cfg     map[string]any
		wantErr string
	}{
		{"valid", map[string]any{"account": "acct", "container": "c"}, ""},
		{"valid camelCase", map[string]any{"accountName": "devstoreaccount1", "container": "c"}, ""},
		{"valid sovereign endpoint", map[string]any{"account": "acct", "container": "c", "endpoint": "https://8.8.8.8/acct"}, ""},
		{"missing container", map[string]any{"account": "acct"}, "required"},
		{"metadata address", map[string]any{"account": "169.254.169.254/x", "container": "c"}, "3-24 lowercase"},
		{"dotted host", map[string]any{"account": "evil.example.com", "container": "c"}, "3-24 lowercase"},
		{"upper case", map[string]any{"account": "Acct", "container": "c"}, "3-24 lowercase"},
		{"too short", map[string]any{"account": "ab", "container": "c"}, "3-24 lowercase"},
		{"too long", map[string]any{"account": strings.Repeat("a", 25), "container": "c"}, "3-24 lowercase"},
		{"loopback endpoint", map[string]any{"account": "acct", "container": "c", "endpoint": "http://127.0.0.1:10000/acct"}, "not allowed"},
		{"metadata endpoint", map[string]any{"account": "acct", "container": "c", "endpoint": "http://169.254.169.254/acct"}, "not allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStorageConfig(context.Background(), "azure", tc.cfg)

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestStorageBackendTestMessagesAreFixed: the GCS and Azure test answers name
// the problem in fixed words and never echo the constructor's error text.
func TestStorageBackendTestMessagesAreFixed(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, kind, config, want, mustNot string
	}{
		{"azure bad key", "azure", `{"account":"acct","container":"c","account_key":"%%not base64%%"}`,
			"the account key is not valid base64", "illegal base64"},
		{"azure bad account", "azure", `{"account":"169.254.169.254/x","container":"c","account_key":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`,
			"account must be 3-24 lowercase letters and digits", "169.254"},
		{"azure bad endpoint", "azure", `{"account":"acct","container":"c","account_key":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=","endpoint":"ftp://secret-host/x"}`,
			"endpoint is not allowed", "secret-host"},
		{"gcs no hmac", "gcs", `{"bucket":"b"}`, "the HMAC key is required", "GCSHMACKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &storageUpdateMockDB{backend: &models.StorageBackend{ID: "b1", Name: "x", Kind: tc.kind, Config: []byte(tc.config)}}

			r := chi.NewRouter()
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsLegacy: true})))
				})
			})
			NewAdminStorageHandler(db, log, nil).Register(r)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/admin/storage/x/test", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, tc.want) || strings.Contains(body, tc.mustNot) {
				t.Fatalf("status = %d, body = %s; want %q and not %q", rec.Code, body, tc.want, tc.mustNot)
			}
		})
	}
}
