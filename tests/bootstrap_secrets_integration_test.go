// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/pkg/logger"
)

const bootstrapManifest = `apiVersion: openartifact.io/v1
kind: User
metadata:
  name: carol
spec:
  email: carol@example.com
`

func writeBootstrapSecret(t *testing.T, name, content string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

// newBootstrapToken builds a token secret in the documented shape without a
// fixed literal in the source.
func newBootstrapToken(t *testing.T) string {
	t.Helper()

	secret, _, err := auth.NewTokenSecret(auth.TokenKindAccess)
	if err != nil {
		t.Fatal(err)
	}

	return secret
}

func (s *declServer) openDB(t *testing.T) *sql.DB {
	t.Helper()

	u := *s.dbURL
	u.Path = "/" + s.dbName

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db
}

// login returns the HTTP status and the must_change_password flag.
func (s *declServer) login(t *testing.T, password string) (int, bool) {
	t.Helper()

	jar, _ := cookiejar.New(nil)
	sess := &session{c: &http.Client{Jar: jar, Timeout: 30 * time.Second}, base: s.url}
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.url+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := sess.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	var user struct {
		MustChangePassword bool `json:"must_change_password"`
	}

	_ = json.NewDecoder(resp.Body).Decode(&user)

	return resp.StatusCode, user.MustChangePassword
}

func TestBootstrapSuppliedSecretsServer(t *testing.T) {
	const password = "supplied-admin-password-1"

	token := newBootstrapToken(t)

	s, stderr := startServerProc(t, []string{
		"OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE=" + writeBootstrapSecret(t, "pw", password+"\n"),
		"OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_FILE=" + writeBootstrapSecret(t, "token", token+"\n"),
		"OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_TTL=1h",
	})
	s.token = token

	// The server only logs after it is healthy; give the bootstrap banner a
	// chance to appear if it were (wrongly) printed.
	if out := stderr.String(); strings.Contains(out, "OPENARTIFACT BOOTSTRAP") || strings.Contains(out, password) || strings.Contains(out, token) {
		t.Fatalf("a supplied secret or the banner was printed:\n%s", out)
	}

	if code, must := s.login(t, password); code != http.StatusOK || must {
		t.Fatalf("login with the supplied password: status %d must_change_password=%v, want 200 false", code, must)
	}

	// The token carries the admin's manage permission.
	s.api(t, http.MethodGet, "/api/v1/users", nil, http.StatusOK)

	manifest := filepath.Join(t.TempDir(), "user.yaml")
	if err := os.WriteFile(manifest, []byte(bootstrapManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	s.expect(t, 0, "ok", "apply", "-f", manifest)

	var tokens int

	if err := s.openDB(t).QueryRowContext(context.Background(),
		`SELECT count(*) FROM auth_tokens WHERE name = 'bootstrap' AND kind = 'access' AND expires_at > now()`).Scan(&tokens); err != nil || tokens != 1 {
		t.Fatalf("bootstrap tokens with expiry = %d (%v), want 1", tokens, err)
	}
}

func TestBootstrapGeneratedPasswordExpiryServer(t *testing.T) {
	s, stderr := startServerProc(t, nil)
	s.password = waitBootstrapPassword(t, stderr)

	if out := stderr.String(); !strings.Contains(out, "valid for 24h0m0s") || !strings.Contains(out, "OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE") {
		t.Fatalf("banner lacks the expiry or the _FILE hint:\n%s", out)
	}

	if code, must := s.login(t, s.password); code != http.StatusOK || !must {
		t.Fatalf("login before expiry: status %d must_change_password=%v, want 200 true", code, must)
	}

	db := s.openDB(t)

	// Move the expiry into the past instead of sleeping.
	if _, err := db.ExecContext(context.Background(),
		`UPDATE auth_users SET password_expires_at = now() - interval '1 second' WHERE username = 'admin'`); err != nil {
		t.Fatal(err)
	}

	if code, _ := s.login(t, s.password); code != http.StatusUnauthorized {
		t.Fatalf("login after expiry: status %d, want 401", code)
	}

	// cmd/resetpw is the documented way out and clears the expiry.
	resetpw := filepath.Join(t.TempDir(), "resetpw")
	goBuild(t, s.root, resetpw, "./cmd/resetpw")

	const next = "reset-by-operator-1"

	cmd := exec.CommandContext(context.Background(), resetpw)
	cmd.Env = append(os.Environ(),
		"DB_HOST="+s.dbURL.Hostname(), "DB_PORT="+s.dbURL.Port(), "DB_USER="+s.dbURL.User.Username(),
		"DB_PASSWORD="+dbPassword(s), "DB_NAME="+s.dbName, "DB_SSLMODE=disable",
		"OPENARTIFACT_RESET_PASSWORD="+next,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resetpw: %v\n%s", err, out)
	}

	var expires sql.NullTime

	if err := db.QueryRowContext(context.Background(), `SELECT password_expires_at FROM auth_users WHERE username = 'admin'`).Scan(&expires); err != nil || expires.Valid {
		t.Fatalf("resetpw left password_expires_at = %v (%v)", expires, err)
	}

	if code, _ := s.login(t, next); code != http.StatusOK {
		t.Fatalf("login after resetpw: status %d, want 200", code)
	}
}

func dbPassword(s *declServer) string {
	pw, _ := s.dbURL.User.Password()

	return pw
}

func TestBootstrapConcurrentReplicasCreateOneAdminAndToken(t *testing.T) {
	pg := freshPostgres(t, 20)
	ctx := context.Background()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatal(err)
	}

	token := newBootstrapToken(t)
	opts := auth.BootstrapOptions{
		AdminTokenFile: writeBootstrapSecret(t, "token", token),
		Stderr:         &syncBuffer{},
	}

	const replicas = 8

	var wg sync.WaitGroup

	errs := make(chan error, replicas)

	for range replicas {
		wg.Go(func() { errs <- auth.BootstrapIfEmpty(ctx, pg, log, opts) })
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
	}

	var users, tokens, assignments int

	row := pg.GetDB().QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM auth_users),
		(SELECT count(*) FROM auth_tokens WHERE name = 'bootstrap'),
		(SELECT count(*) FROM permission_assignments pa JOIN permission_targets pt ON pt.id = pa.permission_target_id WHERE pt.name = 'admin-all')`)
	if err := row.Scan(&users, &tokens, &assignments); err != nil {
		t.Fatal(err)
	}

	if users != 1 || tokens != 1 || assignments != 1 {
		t.Fatalf("users=%d tokens=%d admin-all assignments=%d, want 1 1 1", users, tokens, assignments)
	}

	if _, user, err := auth.ValidateToken(ctx, pg, token); err != nil || user == nil || user.Username != auth.BootstrapUsername {
		t.Fatalf("bootstrap token does not authenticate the admin: user=%v err=%v", user, err)
	}

	// A restart on the now non-empty instance changes nothing, even with new settings.
	opts.AdminTokenFile = writeBootstrapSecret(t, "token2", newBootstrapToken(t))
	opts.AdminPassword = "another-password-123"

	if err := auth.BootstrapIfEmpty(ctx, pg, log, opts); err != nil {
		t.Fatal(err)
	}

	if err := pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM auth_tokens`).Scan(&tokens); err != nil || tokens != 1 {
		t.Fatalf("tokens after restart = %d (%v), want 1", tokens, err)
	}
}

func TestPasswordChangeClearsExpiry(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	if err := auth.BootstrapIfEmpty(ctx, pg, log, auth.BootstrapOptions{
		GeneratedPasswordTTL: time.Hour,
		Now:                  func() time.Time { return now },
		Stderr:               &syncBuffer{},
	}); err != nil {
		t.Fatal(err)
	}

	admin, err := pg.GetUserByUsername(ctx, auth.BootstrapUsername)
	if err != nil || admin == nil {
		t.Fatalf("admin: %v %v", admin, err)
	}

	at, err := pg.GetPasswordExpiry(ctx, admin.ID)
	if err != nil || at == nil || !at.Equal(now.Add(time.Hour)) {
		t.Fatalf("expiry = %v (%v), want %v", at, err, now.Add(time.Hour))
	}

	hash, err := auth.HashPassword("a-new-password-123")
	if err != nil {
		t.Fatal(err)
	}

	if err := pg.UpdateUserPassword(ctx, admin.ID, hash, false); err != nil {
		t.Fatal(err)
	}

	if at, err := pg.GetPasswordExpiry(ctx, admin.ID); err != nil || at != nil {
		t.Fatalf("expiry after a password change = %v (%v), want none", at, err)
	}
}
