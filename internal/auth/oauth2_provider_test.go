// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestNextLink(t *testing.T) {
	tests := []struct{ name, header, want string }{
		{"empty", "", ""},
		{"next and last", `<https://api/x?page=2>; rel="next", <https://api/x?page=9>; rel="last"`, "https://api/x?page=2"},
		{"only last", `<https://api/x?page=9>; rel="last"`, ""},
		{"next is not first", `<https://api/x?page=1>; rel="prev", <https://api/x?page=3>; rel="next"`, "https://api/x?page=3"},
		{"garbage", `rel="next"`, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextLink(tc.header); got != tc.want {
				t.Errorf("nextLink(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestGetAllPagination(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	tests := []struct {
		name    string
		serve   func(self string, page int, w http.ResponseWriter)
		want    int
		wantErr error
	}{
		{
			name: "follows pages until there is no next link",
			serve: func(self string, page int, w http.ResponseWriter) {
				if page < 3 {
					w.Header().Set("Link", `<`+self+`/items?page=`+strconv.Itoa(page+1)+`>; rel="next"`)
				}

				_ = json.NewEncoder(w).Encode([]int{page})
			},
			want: 3,
		},
		{
			name: "never sends the token to another origin",
			serve: func(_ string, _ int, w http.ResponseWriter) {
				w.Header().Set("Link", `<https://attacker.example/items>; rel="next"`)
				_ = json.NewEncoder(w).Encode([]int{1})
			},
			wantErr: errOAuth2ForeignURL,
		},
		{
			name: "endless pagination is an error, not a silent truncation",
			serve: func(self string, _ int, w http.ResponseWriter) {
				w.Header().Set("Link", `<`+self+`/items>; rel="next"`)
				_ = json.NewEncoder(w).Encode([]int{1})
			},
			wantErr: errOAuth2TooManyPages,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				srv  *httptest.Server
				hits int
			)

			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits++
				w.Header().Set("Content-Type", "application/json")
				tc.serve(srv.URL, hits, w)
			}))
			defer srv.Close()

			origin, _ := url.Parse(srv.URL)
			api := &oauth2API{origin: origin, token: "tok"}

			got, err := getAll[int](context.Background(), api, srv.URL+"/items")

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}

				return
			}

			if err != nil || len(got) != tc.want {
				t.Fatalf("got %v, %v; want %d items", got, err, tc.want)
			}
		})
	}
}

func TestGetAllSurfacesUpstreamStatus(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()

	origin, _ := url.Parse(srv.URL)

	_, err := getAll[int](context.Background(), &oauth2API{origin: origin, token: "tok"}, srv.URL)

	var statusErr *apiStatusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusForbidden {
		t.Fatalf("expected an apiStatusError(403), got %v", err)
	}
}

func TestLoadOIDCProviderRow_Types(t *testing.T) {
	tests := []struct {
		name    string
		cfg     models.OIDCConfig
		wantErr error
	}{
		{"unknown type is refused", models.OIDCConfig{Type: "ldap", ClientID: "c", ClientSecret: "s"}, ErrOIDCUnknownType},
		{"oauth2 without urls is invalid", models.OIDCConfig{Type: "oauth2", ClientID: "c", ClientSecret: "s"}, ErrOIDCConfigInvalid},
		{"github loads without any network access", models.OIDCConfig{Type: "github", ClientID: "c", ClientSecret: "s", AllowedOrgs: []string{"a"}}, nil},
		{"multi-tenant authority needs allowed_tenants", models.OIDCConfig{Issuer: "https://login.microsoftonline.com/common/v2.0", ClientID: "c", ClientSecret: "s"}, ErrOIDCConfigInvalid},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.cfg)
			row := &models.SSOProvider{
				ID: "t", Slug: "types-" + strconv.Itoa(i), Kind: "oidc", Enabled: true, OIDCConfig: raw, UpdatedAt: time.Now(),
			}

			p, err := LoadOIDCProviderRow(context.Background(), row, "http://localhost")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}

			if err == nil && !p.IsOAuth2() {
				t.Error("github must load as an OAuth 2.0 provider")
			}

			// Plain OAuth 2.0 providers never act as access-token issuers.
			if _, ok := oidcRowConfig(row); ok && tc.cfg.Type != "" {
				t.Error("oauth2 providers must not be advertised as token issuers")
			}
		})
	}
}
