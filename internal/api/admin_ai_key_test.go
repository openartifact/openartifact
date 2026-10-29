// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !noai

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/pkg/logger"
)

type aiSettingsDBStore interface {
	AdminAIStore
	auth.EvaluatorStore
}

// aiSettingsDB serves fixed system settings.
type aiSettingsDB struct {
	aiSettingsDBStore

	settings map[string]string
}

func (d *aiSettingsDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	return d.settings[key], nil
}

func (d *aiSettingsDB) SetSystemSetting(_ context.Context, key, value string) error {
	d.settings[key] = value

	return nil
}

// recordingLLM is a models endpoint that remembers the Authorization header of
// every request it gets.
type recordingLLM struct {
	srv *httptest.Server

	mu   sync.Mutex
	auth []string
}

func newRecordingLLM(t *testing.T) *recordingLLM {
	t.Helper()

	l := &recordingLLM{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.auth = append(l.auth, r.Header.Get("Authorization"))
		l.mu.Unlock()

		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(l.srv.Close)

	return l
}

func (l *recordingLLM) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.auth...)
}

// TestAdminAITestUsesStoredKeyOnlyForStoredEndpoint: the masked key means "the
// stored one", which must not be sent to an endpoint the admin just typed.
func TestAdminAITestUsesStoredKeyOnlyForStoredEndpoint(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")

	stored, other := newRecordingLLM(t), newRecordingLLM(t)

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		body       string
		target     *recordingLLM
		wantStatus int
		wantAuth   string // Authorization the target must have received
		wantCalls  int
	}{
		{"stored endpoint, masked key", `{"provider":"custom","base_url":"` + stored.srv.URL + `","api_key":"` + aiAPIKeyMask + `"}`,
			stored, http.StatusOK, "Bearer sk-stored", 1},
		{"stored endpoint, trailing slash", `{"provider":"custom","base_url":"` + stored.srv.URL + `/","api_key":"` + aiAPIKeyMask + `"}`,
			stored, http.StatusOK, "Bearer sk-stored", 1},
		{"stored endpoint, key omitted", `{"provider":"custom","base_url":"` + stored.srv.URL + `"}`,
			stored, http.StatusOK, "Bearer sk-stored", 1},
		{"other endpoint, masked key", `{"provider":"custom","base_url":"` + other.srv.URL + `","api_key":"` + aiAPIKeyMask + `"}`,
			other, http.StatusBadRequest, "", 0},
		{"other endpoint, key omitted", `{"provider":"custom","base_url":"` + other.srv.URL + `"}`,
			other, http.StatusBadRequest, "", 0},
		{"other endpoint, new key", `{"provider":"custom","base_url":"` + other.srv.URL + `","api_key":"sk-new"}`,
			other, http.StatusOK, "Bearer sk-new", 1},
		{"other endpoint, keyless", `{"provider":"custom","base_url":"` + other.srv.URL + `","api_key":""}`,
			other, http.StatusOK, "", 1},
		{"provider default endpoint, masked key", `{"provider":"openai","api_key":"` + aiAPIKeyMask + `"}`,
			other, http.StatusBadRequest, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored.mu.Lock()
			stored.auth = nil
			stored.mu.Unlock()
			other.mu.Lock()
			other.auth = nil
			other.mu.Unlock()

			db := &aiSettingsDB{settings: map[string]string{
				ai.SettingProvider: "custom", ai.SettingBaseURL: stored.srv.URL, ai.SettingAPIKey: "sk-stored",
			}}

			r := chi.NewRouter()
			NewAdminAIHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/admin/ai/test", strings.NewReader(tc.body))
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

			rw := httptest.NewRecorder()
			r.ServeHTTP(rw, req)

			if rw.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rw.Code, tc.wantStatus, rw.Body)
			}

			if tc.wantStatus == http.StatusBadRequest && !strings.Contains(rw.Body.String(), `"`+aiAPIKeyRequiredCode+`"`) {
				t.Errorf("error code is not %q: %s", aiAPIKeyRequiredCode, rw.Body)
			}

			got := tc.target.seen()
			if len(got) != tc.wantCalls || (tc.wantCalls == 1 && got[0] != tc.wantAuth) {
				t.Errorf("target saw Authorization %q, want %d call(s) with %q", got, tc.wantCalls, tc.wantAuth)
			}

			if tc.target != stored {
				if n := len(stored.seen()); n != 0 {
					t.Errorf("the stored endpoint was contacted %d times", n)
				}
			}

			if strings.Contains(rw.Body.String(), "sk-stored") {
				t.Errorf("the stored key is in the response: %s", rw.Body)
			}
		})
	}
}

// TestAdminAIPutKeepsStoredKeyOnlyForStoredEndpoint: saving another endpoint
// must not leave the stored key in force for it; the request has to supply a
// key (a new one, or an empty one that clears it).
func TestAdminAIPutKeepsStoredKeyOnlyForStoredEndpoint(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	const stored = "https://llm.example.com/v1"

	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
		wantKey    string // stored key after the call
		wantURL    string
	}{
		{"same endpoint, key masked", `{"base_url":"` + stored + `","model":"m2","api_key":"` + aiAPIKeyMask + `"}`, http.StatusOK, "sk-stored", stored},
		{"same endpoint, trailing slash, key omitted", `{"base_url":"` + stored + `/","model":"m2"}`, http.StatusOK, "sk-stored", stored + "/"},
		{"no endpoint change", `{"model":"m2"}`, http.StatusOK, "sk-stored", stored},
		{"other base_url, key masked", `{"base_url":"https://evil.example.com/v1","api_key":"` + aiAPIKeyMask + `"}`, http.StatusBadRequest, "sk-stored", stored},
		{"other base_url, key omitted", `{"base_url":"https://evil.example.com/v1"}`, http.StatusBadRequest, "sk-stored", stored},
		{"other provider, key omitted", `{"provider":"openai","base_url":""}`, http.StatusBadRequest, "sk-stored", stored},
		{"other base_url, new key", `{"base_url":"https://other.example.com/v1","api_key":"sk-new"}`, http.StatusOK, "enc-or-plain:sk-new", "https://other.example.com/v1"},
		{"other base_url, key cleared", `{"base_url":"https://other.example.com/v1","api_key":""}`, http.StatusOK, "", "https://other.example.com/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &aiSettingsDB{settings: map[string]string{
				ai.SettingEnabled: "true", ai.SettingProvider: "custom", ai.SettingBaseURL: stored, ai.SettingAPIKey: "sk-stored",
			}}

			r := chi.NewRouter()
			NewAdminAIHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/api/v1/admin/ai/config", strings.NewReader(tc.body))
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

			rw := httptest.NewRecorder()
			r.ServeHTTP(rw, req)

			if rw.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rw.Code, tc.wantStatus, rw.Body)
			}

			if tc.wantStatus == http.StatusBadRequest && !strings.Contains(rw.Body.String(), `"`+aiAPIKeyRequiredCode+`"`) {
				t.Errorf("error code is not %q: %s", aiAPIKeyRequiredCode, rw.Body)
			}

			gotKey := db.settings[ai.SettingAPIKey]
			if strings.HasPrefix(tc.wantKey, "enc-or-plain:") {
				if gotKey == "" || gotKey == "sk-stored" {
					t.Errorf("stored key = %q, want the new key", gotKey)
				}
			} else if gotKey != tc.wantKey {
				t.Errorf("stored key = %q, want %q", gotKey, tc.wantKey)
			}

			if got := db.settings[ai.SettingBaseURL]; got != tc.wantURL {
				t.Errorf("stored base_url = %q, want %q", got, tc.wantURL)
			}
		})
	}
}
