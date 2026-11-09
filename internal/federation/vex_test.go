// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

func TestValidVEXTrust(t *testing.T) {
	t.Parallel()

	for _, s := range []string{VEXTrustIgnore, VEXTrustSuggest, VEXTrustApply} {
		if !ValidVEXTrust(s) {
			t.Errorf("ValidVEXTrust(%q) = false", s)
		}
	}

	for _, s := range []string{"", "Apply", "trust", " ignore"} {
		if ValidVEXTrust(s) {
			t.Errorf("ValidVEXTrust(%q) = true", s)
		}
	}
}

func TestClientVEX(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")

	doc := []byte(`{"@context":"https://openvex.dev/ns/v0.2.0","statements":[]}`)

	tests := []struct {
		name    string
		status  int
		body    []byte
		want    []byte
		wantErr func(error) bool
	}{
		{name: "published", status: http.StatusOK, body: doc, want: doc},
		{
			name: "not published", status: http.StatusNotFound, body: []byte(`{"code":"FEDERATION_NOT_FOUND"}`),
			wantErr: func(err error) bool { return errors.Is(err, ErrNotFound) },
		},
		{
			name: "too large", status: http.StatusOK, body: bytes.Repeat([]byte(" "), maxVEXDocument+1),
			wantErr: func(err error) bool { return Describe(err) == ReasonInvalidResponse },
		},
		{
			name: "refused", status: http.StatusUnauthorized, body: []byte(`{}`),
			wantErr: func(err error) bool { return Describe(err) == ReasonUnauthorized+" (HTTP 401)" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/federation/v1/vex" || r.Header.Get("Authorization") != "Bearer secret" {
					http.Error(w, "unexpected request", http.StatusBadRequest)

					return
				}

				w.WriteHeader(tt.status)
				_, _ = w.Write(tt.body)
			}))
			t.Cleanup(srv.Close)

			c, err := NewClient(t.Context(), &Peer{BaseURL: srv.URL}, PeerSecrets{Token: "secret"}, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}

			got, err := c.VEX(t.Context())

			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("VEX() error = %v (%s)", err, Describe(err))
				}

				return
			}

			if err != nil || !bytes.Equal(got, tt.want) {
				t.Fatalf("VEX() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
