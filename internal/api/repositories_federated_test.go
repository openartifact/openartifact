// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fedCreateRepo posts body to the admin create-repository handler as p.
func fedCreateRepo(t *testing.T, h *RepositoriesHandler, p *auth.Principal, body string) (int, map[string]any, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, req)

	var m map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &m)

	return rw.Code, m, rw.Body.String()
}

func fedRepoRowCount(t *testing.T, db *sql.DB, name string) int {
	t.Helper()

	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM repositories WHERE name = $1`, name).Scan(&n); err != nil {
		t.Fatalf("count repositories: %v", err)
	}

	return n
}

func fedRepoFlag(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var f bool
	if err := db.QueryRowContext(context.Background(), `SELECT federated FROM repositories WHERE name = $1`, name).Scan(&f); err != nil {
		t.Fatalf("read federated flag: %v", err)
	}

	return f
}

func newFedRepositoriesHandler(t *testing.T, withFederation bool) (*RepositoriesHandler, *fedAPIEnv) {
	t.Helper()

	e := newFedAPIEnv(t, withFederation)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	h := NewRepositoriesHandler(e.pg, log, e.eval)
	if withFederation {
		h.WithFederation(federation.NewStore(e.db))
	}

	return h, e
}

func TestCreateRepositoryFederated(t *testing.T) {
	for _, format := range []string{"raw", "maven", "npm", "docker", "helm", "pypi", "cargo", "go", "nuget"} {
		t.Run(format, func(t *testing.T) {
			h, e := newFedRepositoriesHandler(t, true)

			code, m, raw := fedCreateRepo(t, h, fedAPIAdmin, `{"name":"fedrepo","format":"`+format+`","federated":true}`)
			if code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body %s", code, raw)
			}

			if m["federated"] != true {
				t.Errorf("federated = %v, want true", m["federated"])
			}

			if !fedRepoFlag(t, e.db, "fedrepo") {
				t.Error("repositories.federated not set")
			}

			if !fedAPILogEnabled(t, e.db, fedAPIRepoID(t, e.db, "fedrepo")) {
				t.Error("federation log not started")
			}
		})
	}
}

func TestCreateRepositoryFederatedFalseIsPlain(t *testing.T) {
	h, e := newFedRepositoriesHandler(t, true)

	for _, body := range []string{
		`{"name":"plain1","format":"raw"}`,
		`{"name":"plain2","format":"raw","federated":false}`,
	} {
		code, m, raw := fedCreateRepo(t, h, fedAPIAdmin, body)
		if code != http.StatusCreated {
			t.Fatalf("%s: status = %d; body %s", body, code, raw)
		}

		if m["federated"] != false {
			t.Errorf("%s: federated = %v, want false", body, m["federated"])
		}
	}

	if fedAPILogEnabled(t, e.db, fedAPIRepoID(t, e.db, "plain1")) {
		t.Error("federation log started for a non-federated repository")
	}

	// A non-federated create works with the feature off too.
	h2, _ := newFedRepositoriesHandler(t, false)
	if code, _, raw := fedCreateRepo(t, h2, fedAPIAdmin, `{"name":"plain3","format":"raw"}`); code != http.StatusCreated {
		t.Errorf("feature off, not federated: %d %s", code, raw)
	}
}

func TestCreateRepositoryFederatedRefused(t *testing.T) {
	tests := []struct {
		name    string
		feature bool
		body    string
	}{
		{"proxy repository", true, `{"name":"x","format":"raw","repo_type":"proxy","upstream_url":"https://93.184.216.34","federated":true}`},
		{"group repository", true, `{"name":"x","format":"raw","repo_type":"group","member_repos":[],"federated":true}`},
		{"unsupported format deb", true, `{"name":"x","format":"deb","federated":true}`},
		{"unsupported format rpm", true, `{"name":"x","format":"rpm","federated":true}`},
		{"feature off", false, `{"name":"x","format":"raw","federated":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e := newFedRepositoriesHandler(t, tt.feature)

			code, m, raw := fedCreateRepo(t, h, fedAPIAdmin, tt.body)
			if code != http.StatusBadRequest || m["code"] != "validation_failed" {
				t.Fatalf("got %d %s, want 400 validation_failed", code, raw)
			}

			if fedRepoRowCount(t, e.db, "x") != 0 {
				t.Error("repository created despite the refusal")
			}
		})
	}
}

func TestCreateRepositoryFederatedRefusalMessages(t *testing.T) {
	h, _ := newFedRepositoriesHandler(t, true)

	_, m, _ := fedCreateRepo(t, h, fedAPIAdmin, `{"name":"x","format":"deb","federated":true}`)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "federation support") {
		t.Errorf("message %q does not explain the supported formats", msg)
	}

	off, _ := newFedRepositoriesHandler(t, false)

	_, m, _ = fedCreateRepo(t, off, fedAPIAdmin, `{"name":"x","format":"raw","federated":true}`)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "federation.enabled") {
		t.Errorf("message %q does not name the server setting", msg)
	}
}

func TestCreateRepositoryFederatedRequiresAdmin(t *testing.T) {
	h, e := newFedRepositoriesHandler(t, true)

	for name, p := range map[string]*auth.Principal{"plain user": fedAPIPlainUser(), "anonymous": fedAPIAnon} {
		t.Run(name, func(t *testing.T) {
			code, m, raw := fedCreateRepo(t, h, p, `{"name":"nope","format":"raw","federated":true}`)
			if code != http.StatusForbidden || m["code"] != "forbidden" {
				t.Fatalf("got %d %s, want 403 forbidden", code, raw)
			}
		})
	}

	if fedRepoRowCount(t, e.db, "nope") != 0 {
		t.Error("repository created by a non-admin")
	}
}

func TestCreateRepositoryFederatedDelegatedActorForbidden(t *testing.T) {
	h, e := newFedRepositoriesHandler(t, true)
	actor := fedAPIPlainUser()

	// The delegated (project) path passes the member as actor; the federated
	// refusal comes before any project policy check, whatever the repository type.
	for _, body := range []string{
		`{"name":"member-repo","format":"raw","federated":true}`,
		`{"name":"member-repo","format":"deb","federated":true}`,
		`{"name":"member-repo","format":"raw","repo_type":"proxy","federated":true}`,
	} {
		var b createRepositoryBody
		if err := json.Unmarshal([]byte(body), &b); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/projects/p/repositories", nil)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), actor))
		rw := httptest.NewRecorder()

		h.createRepository(rw, req, &b, nil, actor)

		if rw.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403; body %s", body, rw.Code, rw.Body.String())
		}

		if strings.Contains(rw.Body.String(), "federation support") {
			t.Errorf("%s: a delegated member learned the format list instead of a plain refusal: %s", body, rw.Body.String())
		}
	}

	if fedRepoRowCount(t, e.db, "member-repo") != 0 {
		t.Error("repository created for a delegated member")
	}
}

func TestCreateRepositoryFederatedEnableFailureDoesNotLeak(t *testing.T) {
	h, e := newFedRepositoriesHandler(t, true)

	// Make Enable fail after the repository row is created: a store on a
	// connection pool that cannot reach the database.
	broken, err := sql.Open("pgx", "postgres://nobody:pw@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = broken.Close() })

	h.WithFederation(federation.NewStore(broken))

	code, m, raw := fedCreateRepo(t, h, fedAPIAdmin, `{"name":"halfway","format":"raw","federated":true}`)
	if code != http.StatusInternalServerError || m["code"] != "internal_error" {
		t.Fatalf("got %d %s, want 500 internal_error", code, raw)
	}

	for _, leak := range []string{"127.0.0.1", "nobody", "pgx", "refused", "dial"} {
		if strings.Contains(strings.ToLower(raw), leak) {
			t.Errorf("error body leaks %q: %s", leak, raw)
		}
	}

	if fedRepoRowCount(t, e.db, "halfway") != 1 {
		t.Fatal("expected the repository row to exist (documented: created but federation not enabled)")
	}

	if fedRepoFlag(t, e.db, "halfway") {
		t.Error("federated flag set although the log was not started")
	}
}
