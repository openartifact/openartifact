// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !noai

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/netutil"
)

// TestClientNeverSendsUndecryptableKey: a stored key that cannot be decrypted
// (secret key rotated or removed) fails the request instead of going out as
// "Bearer enc:v1:...".
func TestClientNeverSendsUndecryptableKey(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")
	t.Cleanup(func() { _ = crypto.Init("") })

	var (
		mu   sync.Mutex
		auth []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()

		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(srv.Close)

	if err := crypto.Init("first-secret-key-0123456789"); err != nil {
		t.Fatal(err)
	}

	sealed, err := crypto.Encrypt("sk-plain")
	if err != nil || !crypto.IsEncrypted(sealed) {
		t.Fatalf("encrypt: %q %v", sealed, err)
	}

	cfg := Config{Provider: "custom", BaseURL: srv.URL, APIKey: sealed, Model: "m"}

	// The right secret key: the plaintext key is the bearer token.
	if _, err := NewClient(cfg).ListModels(context.Background()); err != nil {
		t.Fatalf("decryptable key: %v", err)
	}

	mu.Lock()

	if len(auth) != 1 || auth[0] != "Bearer sk-plain" {
		t.Fatalf("Authorization = %q, want the decrypted key", auth)
	}

	mu.Unlock()

	for _, tc := range []struct{ name, secret string }{
		{"rotated secret key", "second-secret-key-0123456789"},
		{"secret key removed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := crypto.Init(tc.secret); err != nil {
				t.Fatal(err)
			}

			mu.Lock()
			auth = nil
			mu.Unlock()

			c := NewClient(cfg)

			if _, err := c.ListModels(context.Background()); !errors.Is(err, ErrAPIKeyUndecryptable) {
				t.Errorf("ListModels err = %v, want ErrAPIKeyUndecryptable", err)
			}

			if _, _, err := c.Analyze(context.Background(), "manifest"); !errors.Is(err, ErrAPIKeyUndecryptable) {
				t.Errorf("Analyze err = %v, want ErrAPIKeyUndecryptable", err)
			}

			mu.Lock()
			defer mu.Unlock()

			if len(auth) != 0 {
				t.Errorf("requests were sent with Authorization %q", auth)
			}
		})
	}
}
