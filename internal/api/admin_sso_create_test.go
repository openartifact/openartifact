// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openartifact/openartifact/internal/database/models"
)

type ssoCreateMockDB struct {
	AdminSSOStore

	createErr error
}

func (d *ssoCreateMockDB) CreateSSOProvider(context.Context, *models.SSOProvider) error {
	return d.createErr
}

// Creating a provider whose slug is taken is a conflict the client can act on,
// not a server error; any other store failure stays a 500.
func TestAdminSSOCreateMapsStoreErrors(t *testing.T) {
	const body = `{"slug":"okta","kind":"oidc","display_name":"Okta",` +
		`"oidc_config":{"type":"github","client_id":"id","client_secret":"secret","allowed_orgs":["acme"]}}`

	tests := []struct {
		name       string
		createErr  error
		wantStatus int
	}{
		{name: "created", wantStatus: http.StatusCreated},
		{name: "slug taken", createErr: &pgconn.PgError{Code: "23505"}, wantStatus: http.StatusConflict},
		{name: "other failure", createErr: errors.New("boom"), wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &ssoCreateMockDB{createErr: tt.createErr}
			log := newFixTestLogger(t)
			h := NewAdminSSOHandler(db, log, nil, "https://oa.example.test", nil)

			rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/admin/sso-providers", body)
			if rw.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.wantStatus, rw.Body.String())
			}
		})
	}
}
