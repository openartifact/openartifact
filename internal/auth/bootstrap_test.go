// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// bootstrapDB records what BootstrapIfEmpty asks the database to create.
type bootstrapDB struct {
	database.BaseMockDB

	users    int
	specs    []database.BootstrapAdmin
	repaired int
	lost     bool
}

func (m *bootstrapDB) CountUsers(context.Context) (int, error) { return m.users, nil }

func (m *bootstrapDB) RepairBootstrapAdmin(context.Context, string) (bool, error) {
	m.repaired++

	return false, nil
}

func (m *bootstrapDB) CreateBootstrapAdmin(_ context.Context, spec database.BootstrapAdmin) (bool, error) {
	m.specs = append(m.specs, spec)

	return !m.lost, nil
}

func bootstrapLog(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	return log
}

func writeSecret(t *testing.T, content string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

const goodToken = "oa_pat_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestBootstrapSuppliedPasswordPrintsNothing(t *testing.T) {
	const pw = "correct horse battery"

	for name, opts := range map[string]BootstrapOptions{
		"file":  {AdminPasswordFile: writeSecret(t, pw+"\n")},
		"value": {AdminPassword: pw},
	} {
		t.Run(name, func(t *testing.T) {
			db := &bootstrapDB{}

			var stderr bytes.Buffer

			opts.Stderr = &stderr

			if err := BootstrapIfEmpty(t.Context(), db, bootstrapLog(t), opts); err != nil {
				t.Fatal(err)
			}

			if stderr.Len() != 0 {
				t.Errorf("banner printed for a supplied password: %q", stderr.String())
			}

			spec := db.specs[0]
			if spec.MustChangePassword || spec.PasswordExpiresAt != nil {
				t.Errorf("supplied password must not force a change or expire: %+v", spec)
			}

			if ok, err := VerifyPassword(pw, spec.PasswordHash); err != nil || !ok {
				t.Errorf("stored hash does not match the supplied password (trailing newline trimmed): %v", err)
			}
		})
	}
}

func TestBootstrapPasswordFileWinsAndForceFlag(t *testing.T) {
	db := &bootstrapDB{}

	err := BootstrapIfEmpty(t.Context(), db, bootstrapLog(t), BootstrapOptions{
		AdminPasswordFile:   writeSecret(t, "from-the-file-123\n"),
		AdminPassword:       "from-the-env-value-123",
		ForcePasswordChange: true,
		Stderr:              &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	spec := db.specs[0]
	if !spec.MustChangePassword {
		t.Error("forcePasswordChange must force a change of a supplied password")
	}

	if ok, _ := VerifyPassword("from-the-file-123", spec.PasswordHash); !ok {
		t.Error("the file must win over the plain value")
	}
}

func TestBootstrapInvalidSuppliedSecretsFailStartup(t *testing.T) {
	cases := map[string]BootstrapOptions{
		"short password":    {AdminPassword: "short"},
		"short file":        {AdminPasswordFile: writeSecret(t, "short\n")},
		"empty file":        {AdminPasswordFile: writeSecret(t, "\n")},
		"missing file":      {AdminPasswordFile: filepath.Join(t.TempDir(), "nope")},
		"token no prefix":   {AdminTokenFile: writeSecret(t, strings.Repeat("ab12", 16))},
		"token too short":   {AdminTokenFile: writeSecret(t, "oa_pat_abcdef0123456789")},
		"token low entropy": {AdminTokenFile: writeSecret(t, "oa_pat_"+strings.Repeat("a", 64))},
		"token bad chars":   {AdminTokenFile: writeSecret(t, "oa_pat_"+strings.Repeat("abcdefgh1234!", 4))},
		"negative ttl":      {AdminTokenTTL: -time.Hour},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			db := &bootstrapDB{}
			opts.Stderr = &bytes.Buffer{}

			err := BootstrapIfEmpty(t.Context(), db, bootstrapLog(t), opts)
			if err == nil {
				t.Fatal("startup must fail")
			}

			if len(db.specs) != 0 {
				t.Error("nothing may be created when a secret is invalid")
			}

			if name != "negative ttl" && !errors.Is(err, ErrBootstrapSecret) {
				t.Errorf("error %v does not wrap ErrBootstrapSecret", err)
			}
		})
	}
}

func TestBootstrapTokenSpec(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	db := &bootstrapDB{}

	err := BootstrapIfEmpty(t.Context(), db, bootstrapLog(t), BootstrapOptions{
		AdminTokenFile: writeSecret(t, goodToken+"\n"),
		AdminTokenTTL:  48 * time.Hour,
		Now:            func() time.Time { return now },
		Stderr:         &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	spec := db.specs[0]
	if spec.TokenName != "bootstrap" || spec.TokenHash != lookupDigest(strings.TrimPrefix(goodToken, TokenPrefixAccess)) {
		t.Errorf("token spec %+v does not match the file content", spec)
	}

	if spec.TokenExpiresAt == nil || !spec.TokenExpiresAt.Equal(now.Add(48*time.Hour)) {
		t.Errorf("token expiry = %v", spec.TokenExpiresAt)
	}

	// A generated password is still produced when only a token is supplied.
	if !spec.MustChangePassword {
		t.Error("the generated password must force a change")
	}
}

func TestBootstrapGeneratedPasswordBannerAndExpiry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	cases := map[string]struct {
		ttl        time.Duration
		wantExpiry *time.Time
		wantText   string
	}{
		"default 24h": {ttl: 24 * time.Hour, wantExpiry: new(now.Add(24 * time.Hour)), wantText: "valid for 24h0m0s"},
		"never":       {ttl: 0, wantExpiry: nil, wantText: "never expires"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := &bootstrapDB{}

			var stderr bytes.Buffer

			err := BootstrapIfEmpty(t.Context(), db, bootstrapLog(t), BootstrapOptions{
				GeneratedPasswordTTL: tc.ttl,
				Now:                  func() time.Time { return now },
				Stderr:               &stderr,
			})
			if err != nil {
				t.Fatal(err)
			}

			spec := db.specs[0]
			if !spec.MustChangePassword {
				t.Error("generated password must force a change")
			}

			switch {
			case tc.wantExpiry == nil && spec.PasswordExpiresAt != nil:
				t.Errorf("unexpected expiry %v", spec.PasswordExpiresAt)
			case tc.wantExpiry != nil && (spec.PasswordExpiresAt == nil || !spec.PasswordExpiresAt.Equal(*tc.wantExpiry)):
				t.Errorf("expiry = %v, want %v", spec.PasswordExpiresAt, tc.wantExpiry)
			}

			out := stderr.String()
			for _, want := range []string{"OPENARTIFACT BOOTSTRAP", "admin password: ", tc.wantText, "OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE"} {
				if !strings.Contains(out, want) {
					t.Errorf("banner lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestBootstrapLosingReplicaPrintsNothing(t *testing.T) {
	db := &bootstrapDB{lost: true}

	var stderr bytes.Buffer

	if err := BootstrapIfEmpty(t.Context(), db, bootstrapLog(t), BootstrapOptions{Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}

	if stderr.Len() != 0 {
		t.Errorf("replica that lost the race printed %q", stderr.String())
	}
}

func TestBootstrapNonEmptyInstanceIgnoresSettings(t *testing.T) {
	db := &bootstrapDB{users: 3}

	var stderr bytes.Buffer

	// Even unreadable or invalid settings are ignored: nothing is read.
	err := BootstrapIfEmpty(t.Context(), db, bootstrapLog(t), BootstrapOptions{
		AdminPasswordFile: filepath.Join(t.TempDir(), "missing"),
		AdminPassword:     "short",
		AdminTokenFile:    filepath.Join(t.TempDir(), "missing"),
		Stderr:            &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(db.specs) != 0 || stderr.Len() != 0 {
		t.Errorf("non-empty instance must not be touched: specs=%v stderr=%q", db.specs, stderr.String())
	}

	if db.repaired != 1 {
		t.Errorf("repair path ran %d times, want 1", db.repaired)
	}
}
