// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/pkg/logger"
)

// attestMemSettings is an in-memory attest.SettingsStore.
type attestMemSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *attestMemSettings) GetSystemSetting(_ context.Context, k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.m[k], nil
}

func (s *attestMemSettings) CreateSystemSettingIfAbsent(_ context.Context, k, v string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.m[k]; ok {
		return false, nil
	}

	s.m[k] = v

	return true, nil
}

// TestAttestationWithoutSecretKeyAnswersConflict: with no OPENARTIFACT_SECRET_KEY
// a generated signing key would be stored as plaintext, so every endpoint that
// needs it answers 409 secret_key_required and nothing is stored.
func TestAttestationWithoutSecretKeyAnswersConflict(t *testing.T) {
	mustNoError(t, crypto.Init(""))

	log, err := logger.New("error", "console", "stderr")
	mustNoError(t, err)

	store := &attestMemSettings{m: map[string]string{}}
	f := newAttestFixture(t, false)
	f.mount(log, auth.NewEvaluator(f.db, log, false), sbom.NewGenerator(f.db, nil), attest.NewService("", store))
	f.storeSBOM(t, "spdx-json", "application/json", `{"spdxVersion":"SPDX-2.3"}`)

	for _, tc := range []struct {
		name, target string
		admin        bool
	}{
		{"public key", AttestationKeyPath, false},
		{"admin info", "/api/v1/admin/attestation", true},
		{"sbom attest", "/api/v1/repositories/libs/sbom/" + attestSBOMID + "/download?attest=true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rw := f.get(t, tc.target, tc.admin)
			expectEqual(t, http.StatusConflict, rw.Code)
			expectContains(t, rw.Body.String(), "secret_key_required")
			expectNotContains(t, rw.Body.String(), "PRIVATE")
		})
	}

	expectEqual(t, 0, len(store.m))
}
