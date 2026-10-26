// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type authPurgeFixture struct {
	*purgeSeed

	human string
	hook  string
	seq   int
}

func newAuthPurgeFixture(t *testing.T) *authPurgeFixture {
	t.Helper()

	f := &authPurgeFixture{purgeSeed: newPurgeSeed(t)}

	scan := func(dst *string, q string, args ...any) {
		if err := f.pg.GetDB().QueryRowContext(context.Background(), q, args...).Scan(dst); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}

	scan(&f.human, `INSERT INTO auth_users (username) VALUES ('alice') RETURNING id`)
	scan(&f.hook, `INSERT INTO webhooks (name, url) VALUES ('h', 'http://example.invalid') RETURNING id`)

	return f
}

// serviceUser inserts a project token service account.
func (f *authPurgeFixture) serviceUser(name string) string {
	f.t.Helper()

	var id string
	if err := f.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO auth_users (username, kind, project_id) VALUES ($1, 'service', $2) RETURNING id`, name, trashTestOrgID).Scan(&id); err != nil {
		f.t.Fatal(err)
	}

	return id
}

// session inserts a session of the human expiring in d (negative: expired).
func (f *authPurgeFixture) session(d time.Duration) {
	f.t.Helper()

	f.seq++
	f.exec(`INSERT INTO auth_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + make_interval(secs => $3))`,
		f.human, fmt.Sprintf("s%d", f.seq), d.Seconds())
}

// token inserts a token of user expiring/revoked relative to now (nil: never).
func (f *authPurgeFixture) token(user string, expires, revoked *time.Duration) string {
	f.t.Helper()

	f.seq++

	var id string

	secs := func(d *time.Duration) any {
		if d == nil {
			return nil
		}

		return d.Seconds()
	}

	if err := f.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO auth_tokens (user_id, name, kind, token_hash, expires_at, revoked_at)
		 VALUES ($1, 't', 'access', $2,
		         CASE WHEN $3::float8 IS NULL THEN NULL ELSE now() + make_interval(secs => $3) END,
		         CASE WHEN $4::float8 IS NULL THEN NULL ELSE now() + make_interval(secs => $4) END) RETURNING id`,
		user, fmt.Sprintf("t%d", f.seq), secs(expires), secs(revoked)).Scan(&id); err != nil {
		f.t.Fatal(err)
	}

	return id
}

func (f *authPurgeFixture) delivery(status string, age time.Duration) string {
	f.t.Helper()

	var id string
	if err := f.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO webhook_deliveries (webhook_id, event_type, payload, status, created_at)
		 VALUES ($1, 'e', '{}', $2, now() - make_interval(secs => $3)) RETURNING id`, f.hook, status, age.Seconds()).Scan(&id); err != nil {
		f.t.Fatal(err)
	}

	return id
}

func (f *authPurgeFixture) exists(table, id string) bool {
	f.t.Helper()

	return f.count(`SELECT COUNT(*) FROM `+table+` WHERE id = $1`, id) == 1
}

// TestAuthPurgeRemovesOnlyDeadCredentials: expired sessions, tokens a month
// past expiry or revocation and the service accounts left without a token go;
// everything still usable (or within the 30 day grace) stays.
func TestAuthPurgeRemovesOnlyDeadCredentials(t *testing.T) {
	f := newAuthPurgeFixture(t)

	const day = 24 * time.Hour

	f.session(-time.Minute)
	f.session(time.Hour)

	expiredToken := f.token(f.human, new(-31*day), nil)
	revokedToken := f.token(f.human, nil, new(-31*day))
	recentlyExpired := f.token(f.human, new(-29*day), nil)
	recentlyRevoked := f.token(f.human, nil, new(-time.Hour))
	live := f.token(f.human, new(day), nil)
	forever := f.token(f.human, nil, nil)

	// A service account whose only token is purged goes with it; one that still
	// has a (grace period) token, and one that has none yet... none yet is gone
	// too: tokens are created with their account, an empty account is debris.
	deadSvc := f.serviceUser("svc-dead")
	f.token(deadSvc, new(-40*day), nil)
	graceSvc := f.serviceUser("svc-grace")
	f.token(graceSvc, nil, new(-time.Hour))
	liveSvc := f.serviceUser("svc-live")
	f.token(liveSvc, nil, nil)

	f.delivery("success", 90*day)

	w := NewAuthPurgeWorker(f.pg, testLogger(t))
	defer w.Stop()

	w.purgeSafely()

	if n := f.count(`SELECT COUNT(*) FROM auth_sessions`); n != 1 {
		t.Errorf("%d sessions left, want only the unexpired one", n)
	}

	for name, id := range map[string]string{"expired": expiredToken, "revoked": revokedToken} {
		if f.exists("auth_tokens", id) {
			t.Errorf("%s token older than 30 days survived", name)
		}
	}

	for name, id := range map[string]string{"recently expired": recentlyExpired, "recently revoked": recentlyRevoked, "live": live, "never expiring": forever} {
		if !f.exists("auth_tokens", id) {
			t.Errorf("%s token was purged", name)
		}
	}

	if f.exists("auth_users", deadSvc) {
		t.Error("service account without tokens survived")
	}

	if !f.exists("auth_users", graceSvc) || !f.exists("auth_users", liveSvc) || !f.exists("auth_users", f.human) {
		t.Error("an account with a token, or a human, was purged")
	}

	// Retention is off by default: deliveries stay.
	if n := f.count(`SELECT COUNT(*) FROM webhook_deliveries`); n != 1 {
		t.Errorf("deliveries = %d, want 1 (retention off)", n)
	}
}

// TestAuthPurgeWebhookDeliveryRetention: finished deliveries past the
// retention go; pending ones (the retry loop's) and recent ones stay.
func TestAuthPurgeWebhookDeliveryRetention(t *testing.T) {
	f := newAuthPurgeFixture(t)

	const day = 24 * time.Hour

	oldOK := f.delivery("success", 31*day)
	oldFailed := f.delivery("failed", 31*day)
	oldPending := f.delivery("pending", 31*day)
	recentOK := f.delivery("success", 29*day)

	w := NewAuthPurgeWorker(f.pg, testLogger(t))
	defer w.Stop()

	w.SetWebhookDeliveryRetention(30)
	w.purgeSafely()

	if f.exists("webhook_deliveries", oldOK) || f.exists("webhook_deliveries", oldFailed) {
		t.Error("finished delivery past retention survived")
	}

	if !f.exists("webhook_deliveries", oldPending) || !f.exists("webhook_deliveries", recentOK) {
		t.Error("a pending or in-retention delivery was purged")
	}
}

// TestAuthPurgeStartRunsAPassAndStops: Start purges right away and Stop
// returns promptly.
func TestAuthPurgeStartRunsAPassAndStops(t *testing.T) {
	f := newAuthPurgeFixture(t)

	f.session(-time.Minute)

	w := NewAuthPurgeWorker(f.pg, testLogger(t))
	w.Start()

	pollUntil(t, "the expired session to be purged", func() bool {
		return f.count(`SELECT COUNT(*) FROM auth_sessions`) == 0
	})

	done := make(chan struct{})

	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Stop did not return")
	}
}
