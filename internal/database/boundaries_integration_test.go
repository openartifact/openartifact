// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func holdsAdminAll(t *testing.T, pg *PostgresDB, userID string) bool {
	t.Helper()

	targets, err := pg.GetPermissionTargetsForPrincipalUncached(context.Background(), userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, pt := range targets {
		if pt.Name == "admin-all" {
			return true
		}
	}

	return false
}

func TestCreateBootstrapAdmin(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{"creates the admin with admin-all, an expiring password and a token", testCreateBootstrapAdminCreatesTheAdminWithAdminAll},
		{"a password that must not change has no expiry", testCreateBootstrapAdminAPasswordThatMustNotChange},
		{"an instance that has users is left alone", testCreateBootstrapAdminAnInstanceThatHasUsersIs},
		{"replicas racing on first boot create exactly one admin", testCreateBootstrapAdminReplicasRacingOnFirstBootCreate},
		{"a missing admin-all target aborts without leaving a user", testCreateBootstrapAdminAMissingAdminAllTargetAborts},
	} {
		t.Run(tc.name, tc.run)
	}
}

func testCreateBootstrapAdminCreatesTheAdminWithAdminAll(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	pg := newQuotaTestDB(t, 4)
	pwExp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	tokExp := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	created, err := pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{
		Username: "admin", PasswordHash: "hash", MustChangePassword: true, PasswordExpiresAt: &pwExp,
		TokenHash: "tokhash", TokenName: "bootstrap", TokenExpiresAt: &tokExp,
	})
	if err != nil || !created {
		t.Fatalf("CreateBootstrapAdmin = %v, %v", created, err)
	}

	u, err := pg.GetUserByUsername(ctx, "admin")
	if err != nil || u == nil {
		t.Fatalf("GetUserByUsername = %v, %v", u, err)
	}

	if !u.MustChangePassword || !holdsAdminAll(t, pg, u.ID) {
		t.Errorf("admin: must_change=%v admin-all=%v, want true/true", u.MustChangePassword, holdsAdminAll(t, pg, u.ID))
	}

	exp, err := pg.GetPasswordExpiry(ctx, u.ID)
	if err != nil || exp == nil || !exp.Equal(pwExp) {
		t.Errorf("GetPasswordExpiry = %v, %v; want %v", exp, err, pwExp)
	}

	tok, err := pg.GetTokenByHashUncached(ctx, "tokhash")
	if err != nil || tok == nil {
		t.Fatalf("token = %v, %v", tok, err)
	}

	if tok.UserID != u.ID || tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(tokExp) {
		t.Errorf("token = %+v, want the admin's token expiring %v", tok, tokExp)
	}
}

func testCreateBootstrapAdminAPasswordThatMustNotChange(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	pg := newQuotaTestDB(t, 4)

	if created, err := pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "admin", PasswordHash: "h"}); err != nil || !created {
		t.Fatalf("CreateBootstrapAdmin = %v, %v", created, err)
	}

	u, _ := pg.GetUserByUsername(ctx, "admin")

	if exp, err := pg.GetPasswordExpiry(ctx, u.ID); err != nil || exp != nil {
		t.Errorf("GetPasswordExpiry = %v, %v; want nil, nil", exp, err)
	}

	if tok, err := pg.GetTokenByHashUncached(ctx, ""); err != nil || tok != nil {
		t.Errorf("a token was created although none was requested: %v, %v", tok, err)
	}
}

func testCreateBootstrapAdminAnInstanceThatHasUsersIs(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	pg := newQuotaTestDB(t, 4)

	if created, err := pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "admin", PasswordHash: "first"}); err != nil || !created {
		t.Fatalf("first CreateBootstrapAdmin = %v, %v", created, err)
	}

	created, err := pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "other", PasswordHash: "second", TokenHash: "t2", TokenName: "x"})
	if err != nil || created {
		t.Fatalf("second CreateBootstrapAdmin = %v, %v; want false, nil", created, err)
	}

	if u, _ := pg.GetUserByUsername(ctx, "other"); u != nil {
		t.Error("the losing call created a user")
	}

	if tok, _ := pg.GetTokenByHashUncached(ctx, "t2"); tok != nil {
		t.Error("the losing call created a token")
	}
}

func testCreateBootstrapAdminReplicasRacingOnFirstBootCreate(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	pg := newQuotaTestDB(t, 8)

	var (
		wg      sync.WaitGroup
		winners atomic.Int32
		failed  atomic.Int32
	)

	for i := range 6 {
		wg.Go(func() {
			created, err := pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "admin" + string(rune('a'+i)), PasswordHash: "h"})
			if err != nil {
				failed.Add(1)
			}

			if created {
				winners.Add(1)
			}
		})
	}

	wg.Wait()

	if winners.Load() != 1 || failed.Load() != 0 {
		t.Errorf("winners = %d, errors = %d; want 1 and 0", winners.Load(), failed.Load())
	}

	if n, _ := pg.CountUsers(ctx); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func testCreateBootstrapAdminAMissingAdminAllTargetAborts(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	pg := newQuotaTestDB(t, 4)

	if _, err := pg.GetDB().ExecContext(ctx, `DELETE FROM permission_targets WHERE name = 'admin-all'`); err != nil {
		t.Fatal(err)
	}

	created, err := pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "admin", PasswordHash: "h", TokenHash: "t", TokenName: "n"})
	if err == nil || created {
		t.Fatalf("CreateBootstrapAdmin = %v, %v; want an error", created, err)
	}

	if n, _ := pg.CountUsers(ctx); n != 0 {
		t.Errorf("users = %d after the failed bootstrap, want 0 (rolled back)", n)
	}
}

func TestRepairBootstrapAdmin(t *testing.T) {
	ctx := context.Background()

	strip := func(t *testing.T, pg *PostgresDB) {
		t.Helper()

		if _, err := pg.GetDB().ExecContext(ctx, `DELETE FROM permission_assignments`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("re-binds the local admin when nobody holds admin-all, once", func(t *testing.T) {
		pg := newQuotaTestDB(t, 4)
		_, _ = pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "admin", PasswordHash: "h"})
		u, _ := pg.GetUserByUsername(ctx, "admin")
		strip(t, pg)

		if repaired, err := pg.RepairBootstrapAdmin(ctx, "admin"); err != nil || !repaired {
			t.Fatalf("RepairBootstrapAdmin = %v, %v", repaired, err)
		}

		if !holdsAdminAll(t, pg, u.ID) {
			t.Error("admin does not hold admin-all after the repair")
		}

		if repaired, err := pg.RepairBootstrapAdmin(ctx, "admin"); err != nil || repaired {
			t.Errorf("second RepairBootstrapAdmin = %v, %v; want false, nil", repaired, err)
		}
	})

	t.Run("never touches an instance where another principal holds admin-all", func(t *testing.T) {
		pg := newQuotaTestDB(t, 4)
		_, _ = pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "admin", PasswordHash: "h"})
		admin, _ := pg.GetUserByUsername(ctx, "admin")

		hash := "x"
		other, err := pg.CreateUser(ctx, &models.User{Username: "ops", PasswordHash: &hash})
		if err != nil {
			t.Fatal(err)
		}

		strip(t, pg)

		if _, err := pg.GetDB().ExecContext(ctx,
			`INSERT INTO permission_assignments (permission_target_id, principal_type, principal_id)
			 SELECT id, 'user', $1 FROM permission_targets WHERE name = 'admin-all'`, other.ID); err != nil {
			t.Fatal(err)
		}

		if repaired, err := pg.RepairBootstrapAdmin(ctx, "admin"); err != nil || repaired {
			t.Fatalf("RepairBootstrapAdmin = %v, %v; want false, nil", repaired, err)
		}

		if holdsAdminAll(t, pg, admin.ID) {
			t.Error("the repair granted admin-all although another principal held it")
		}
	})

	t.Run("refuses a disabled, SSO-managed or unknown account", func(t *testing.T) {
		pg := newQuotaTestDB(t, 4)
		_, _ = pg.CreateBootstrapAdmin(ctx, BootstrapAdmin{Username: "admin", PasswordHash: "h"})
		strip(t, pg)

		hash, ext, slug := "x", "ext-1", "okta"

		if _, err := pg.CreateUser(
			ctx,
			&models.User{Username: "sso-admin", PasswordHash: &hash, ExternalID: &ext, ProviderSlug: &slug},
		); err != nil {
			t.Fatal(err)
		}

		if repaired, _ := pg.RepairBootstrapAdmin(ctx, "sso-admin"); repaired {
			t.Error("an SSO-provisioned account was promoted to admin-all")
		}

		if repaired, _ := pg.RepairBootstrapAdmin(ctx, "nobody"); repaired {
			t.Error("an unknown username was repaired")
		}

		if _, err := pg.GetDB().ExecContext(ctx, `UPDATE auth_users SET disabled = true WHERE username = 'admin'`); err != nil {
			t.Fatal(err)
		}

		if repaired, _ := pg.RepairBootstrapAdmin(ctx, "admin"); repaired {
			t.Error("a disabled admin was re-bound to admin-all")
		}
	})
}

func TestConsumeSAMLAssertion(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	later := time.Now().Add(time.Hour)

	first, err := pg.ConsumeSAMLAssertion(ctx, "idp", "_abc", later)
	if err != nil || !first {
		t.Fatalf("first consume = %v, %v", first, err)
	}

	if again, err := pg.ConsumeSAMLAssertion(ctx, "idp", "_abc", later); err != nil || again {
		t.Errorf("replay = %v, %v; want false, nil", again, err)
	}

	if other, err := pg.ConsumeSAMLAssertion(ctx, "other-idp", "_abc", later); err != nil || !other {
		t.Errorf("same id on another provider = %v, %v; want true", other, err)
	}

	// A record past its expiry is purged, after which the id is consumable
	// again; the assertion itself is rejected by its own NotOnOrAfter.
	if ok, err := pg.ConsumeSAMLAssertion(ctx, "idp", "_old", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("consume of an already-expired record = %v, %v", ok, err)
	}

	if ok, err := pg.ConsumeSAMLAssertion(ctx, "idp", "_old", later); err != nil || !ok {
		t.Errorf("consume after the record expired = %v, %v; want true", ok, err)
	}

	// The live record must survive the purge.
	if again, _ := pg.ConsumeSAMLAssertion(ctx, "idp", "_abc", later); again {
		t.Error("the purge of expired records removed a live one")
	}
}

func TestDeleteOtherSessionsByUser(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	hash := "x"

	alice, err := pg.CreateUser(ctx, &models.User{Username: "alice", PasswordHash: &hash})
	if err != nil {
		t.Fatal(err)
	}

	bob, err := pg.CreateUser(ctx, &models.User{Username: "bob", PasswordHash: &hash})
	if err != nil {
		t.Fatal(err)
	}

	exp := time.Now().Add(time.Hour)

	for _, s := range []struct{ user, hash string }{{alice.ID, "a1"}, {alice.ID, "a2"}, {alice.ID, "a3"}, {bob.ID, "b1"}} {
		if _, err := pg.CreateSession(ctx, &models.Session{UserID: s.user, TokenHash: s.hash, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}

	if err := pg.DeleteOtherSessionsByUser(ctx, alice.ID, "a2"); err != nil {
		t.Fatal(err)
	}

	for hash, want := range map[string]bool{"a1": false, "a2": true, "a3": false, "b1": true} {
		s, err := pg.GetSessionByTokenHash(ctx, hash)
		if err != nil {
			t.Fatal(err)
		}

		if (s != nil) != want {
			t.Errorf("session %s present = %v, want %v", hash, s != nil, want)
		}
	}

	if err := pg.DeleteSessionsByUser(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}

	if s, _ := pg.GetSessionByTokenHash(ctx, "a2"); s != nil {
		t.Error("DeleteSessionsByUser left a session behind")
	}

	if s, _ := pg.GetSessionByTokenHash(ctx, "b1"); s == nil {
		t.Error("DeleteSessionsByUser removed another user's session")
	}
}

func checkVEXListing(t *testing.T, pg *PostgresDB, repoA string) {
	t.Helper()

	ctx := context.Background()

	all, err := pg.ListVEXStatements(ctx, repoA, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("ListVEXStatements(repoA) = %d, %v; want 3 (repoB's is not included)", len(all), err)
	}

	order := make([]string, len(all))
	for i, s := range all {
		order[i] = s.ArtifactID + "/" + s.VulnID
	}

	if !slices.Equal(order, []string{"pkg:1/CVE-1", "pkg:1/CVE-2", "pkg:2/CVE-1"}) {
		t.Errorf("list order = %v, want by artifact then vulnerability", order)
	}

	if one, _ := pg.ListVEXStatements(ctx, repoA, "pkg:2"); len(one) != 1 || one[0].VulnID != "CVE-1" {
		t.Errorf("artifact filter returned %+v", one)
	}

	if none, err := pg.ListVEXStatements(ctx, repoA, "pkg:absent"); err != nil || none == nil || len(none) != 0 {
		t.Errorf("empty result = %v, %v; want an empty non-nil slice", none, err)
	}
}

func TestVEXStatements(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repoA := newQuotaRepo(t, pg, "vex-a", 0, 0)
	repoB := newQuotaRepo(t, pg, "vex-b", 0, 0)

	stmt := func(repo, artifact, vuln, status, just string) *models.VEXStatement {
		return &models.VEXStatement{
			RepositoryID:  repo,
			ArtifactID:    artifact,
			VulnID:        vuln,
			Status:        status,
			Justification: just,
			CreatedBy:     "alice",
		}
	}

	first := stmt(repoA, "pkg:1", "CVE-1", "under_investigation", "")
	if err := pg.UpsertVEXStatement(ctx, first); err != nil || first.ID == "" {
		t.Fatalf("UpsertVEXStatement = %v (id %q)", err, first.ID)
	}

	// The same target is replaced, not duplicated, and keeps its id.
	again := stmt(repoA, "pkg:1", "CVE-1", "not_affected", "component_not_present")
	again.CreatedBy = "bob"

	if err := pg.UpsertVEXStatement(ctx, again); err != nil {
		t.Fatal(err)
	}

	if again.ID != first.ID || again.Status != "not_affected" || again.CreatedBy != "bob" || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("upsert replaced the row wrongly: first=%+v again=%+v", first, again)
	}

	for _, s := range []*models.VEXStatement{stmt(repoA, "pkg:1", "CVE-2", "affected", ""), stmt(repoA, "pkg:2", "CVE-1", "fixed", ""), stmt(repoB, "pkg:1", "CVE-1", "affected", "")} {
		if err := pg.UpsertVEXStatement(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	checkVEXListing(t, pg, repoA)

	t.Run("constraints reject unjustified not_affected and unknown status", func(t *testing.T) {
		for _, bad := range []*models.VEXStatement{
			stmt(repoA, "pkg:3", "CVE-9", "not_affected", ""),
			stmt(repoA, "pkg:3", "CVE-9", "maybe", ""),
			stmt(repoA, "pkg:3", "CVE-9", "affected", "made_up"),
		} {
			if err := pg.UpsertVEXStatement(ctx, bad); err == nil {
				t.Errorf("accepted %s/%s", bad.Status, bad.Justification)
			}
		}
	})

	t.Run("delete is scoped to the repository", func(t *testing.T) {
		if err := pg.DeleteVEXStatement(ctx, repoB, first.ID); !errors.Is(err, ErrVEXStatementNotFound) {
			t.Fatalf("delete through another repository = %v, want ErrVEXStatementNotFound", err)
		}

		if err := pg.DeleteVEXStatement(ctx, repoA, first.ID); err != nil {
			t.Fatal(err)
		}

		if err := pg.DeleteVEXStatement(ctx, repoA, first.ID); !errors.Is(err, ErrVEXStatementNotFound) {
			t.Errorf("second delete = %v, want ErrVEXStatementNotFound", err)
		}
	})
}

type presignEnv struct {
	pg        *PostgresDB
	repo      string
	otherRepo string
	sha       string
}

func (e presignEnv) newUpload(t *testing.T, user string, expires time.Time) *models.PresignedUpload {
	t.Helper()

	u := &models.PresignedUpload{RepositoryID: e.repo, Path: "a/b", SizeBytes: 1, SHA256: e.sha, CreatedBy: user, ExpiresAt: expires}
	if err := e.pg.CreatePresignedUpload(context.Background(), u); err != nil {
		t.Fatal(err)
	}

	return u
}

func TestPresignedUploadLifecycle(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	env := presignEnv{
		pg:        pg,
		repo:      newQuotaRepo(t, pg, "presign", 0, 0),
		otherRepo: newQuotaRepo(t, pg, "presign-other", 0, 0),
		sha:       strings.Repeat("ab", 32),
	}

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, env presignEnv)
	}{
		{"ids that are not uuids are never found, claimed or an error", testPresignedIdsThatAreNotUuidsAre},
		{"an upload of another repository is invisible and cannot be claimed", testPresignedAnUploadOfAnotherRepositoryIs},
		{"the complete lease is exclusive until released, expired or completed", testPresignedTheCompleteLeaseIsExclusiveUntil},
		{"only one of many parallel claims wins", testPresignedOnlyOneOfManyParallelClaims},
		{"expiry listing skips live uploads and uploads whose complete is running", testPresignedExpiryListingSkipsLiveUploadsAnd},
		{"the expiry only moves forward and never revives a completed upload", testPresignedTheExpiryOnlyMovesForwardAnd},
		{"existing ids drop unknown and malformed ones", testPresignedExistingIdsDropUnknownAndMalformed},
		{"the per-user cap counts only live unfinished uploads", testPresignedThePerUserCapCountsOnly},
		{"constraints reject a negative size and a malformed digest", testPresignedConstraintsRejectANegativeSizeAnd},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, env) })
	}
}

func testPresignedIdsThatAreNotUuidsAre(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	if u, err := env.pg.GetPresignedUpload(ctx, env.repo, "../etc/passwd"); u != nil || err != nil {
		t.Errorf("Get = %v, %v; want nil, nil", u, err)
	}

	if ok, err := env.pg.ClaimPresignedUpload(ctx, env.repo, "nope", time.Minute); ok || err != nil {
		t.Errorf("Claim = %v, %v; want false, nil", ok, err)
	}

	if ids, err := env.pg.ExistingPresignedUploadIDs(ctx, []string{"x", "y"}); len(ids) != 0 || err != nil {
		t.Errorf("Existing = %v, %v; want none", ids, err)
	}
}

func testPresignedAnUploadOfAnotherRepositoryIs(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	u := env.newUpload(t, "alice", time.Now().Add(time.Hour))

	if got, err := env.pg.GetPresignedUpload(ctx, env.otherRepo, u.ID); got != nil || err != nil {
		t.Errorf("Get through another repository = %v, %v", got, err)
	}

	if ok, _ := env.pg.ClaimPresignedUpload(ctx, env.otherRepo, u.ID, time.Minute); ok {
		t.Error("claimed an upload of another repository")
	}
}

func testPresignedTheCompleteLeaseIsExclusiveUntil(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	u := env.newUpload(t, "alice", time.Now().Add(time.Hour))

	if ok, err := env.pg.ClaimPresignedUpload(ctx, env.repo, u.ID, time.Hour); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}

	if ok, _ := env.pg.ClaimPresignedUpload(ctx, env.repo, u.ID, time.Hour); ok {
		t.Error("a second claim succeeded while the lease was held")
	}

	if err := env.pg.ReleasePresignedUpload(ctx, u.ID); err != nil {
		t.Fatal(err)
	}

	if ok, _ := env.pg.ClaimPresignedUpload(ctx, env.repo, u.ID, time.Hour); !ok {
		t.Fatal("claim after release failed")
	}

	// A lease shorter than the time since the claim counts as expired.
	deadline := time.Now().Add(5 * time.Second)

	for {
		ok, _ := env.pg.ClaimPresignedUpload(ctx, env.repo, u.ID, 10*time.Millisecond)
		if ok {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("an expired lease could not be taken over")
		}

		time.Sleep(5 * time.Millisecond)
	}

	if err := env.pg.MarkPresignedUploadCompleted(ctx, u.ID, "artifact-1"); err != nil {
		t.Fatal(err)
	}

	if ok, _ := env.pg.ClaimPresignedUpload(ctx, env.repo, u.ID, time.Nanosecond); ok {
		t.Error("a completed upload could be claimed again")
	}

	got, _ := env.pg.GetPresignedUpload(ctx, env.repo, u.ID)
	if got == nil || got.CompletedAt == nil || got.CompletingSince != nil || got.ArtifactID != "artifact-1" {
		t.Errorf("completed upload = %+v", got)
	}

	// Releasing must not reopen a completed upload.
	_ = env.pg.ReleasePresignedUpload(ctx, u.ID)

	if got, _ := env.pg.GetPresignedUpload(ctx, env.repo, u.ID); got == nil || got.CompletedAt == nil {
		t.Error("release reopened a completed upload")
	}
}

func testPresignedOnlyOneOfManyParallelClaims(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	u := env.newUpload(t, "alice", time.Now().Add(time.Hour))

	var (
		wg   sync.WaitGroup
		wins atomic.Int32
	)

	for range 8 {
		wg.Go(func() {
			if ok, _ := env.pg.ClaimPresignedUpload(ctx, env.repo, u.ID, time.Hour); ok {
				wins.Add(1)
			}
		})
	}

	wg.Wait()

	if wins.Load() != 1 {
		t.Errorf("%d claims won, want 1", wins.Load())
	}
}

func testPresignedExpiryListingSkipsLiveUploadsAnd(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	expired := env.newUpload(t, "alice", time.Now().Add(-time.Hour))
	live := env.newUpload(t, "alice", time.Now().Add(time.Hour))
	busy := env.newUpload(t, "alice", time.Now().Add(-time.Hour))

	if ok, _ := env.pg.ClaimPresignedUpload(ctx, env.repo, busy.ID, time.Hour); !ok {
		t.Fatal("claim failed")
	}

	ids, err := env.pg.ListExpiredPresignedUploads(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(ids, expired.ID) {
		t.Error("an expired upload is not listed")
	}

	if slices.Contains(ids, live.ID) || slices.Contains(ids, busy.ID) {
		t.Errorf("listed a live or in-progress upload: %v", ids)
	}

	limited, _ := env.pg.ListExpiredPresignedUploads(ctx, 1)
	if len(limited) != 1 {
		t.Errorf("limit 1 returned %d ids", len(limited))
	}
}

func testPresignedTheExpiryOnlyMovesForwardAnd(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	soon := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	u := env.newUpload(t, "alice", soon)

	if err := env.pg.ExtendPresignedUploadExpiry(ctx, u.ID, soon.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if got, _ := env.pg.GetPresignedUpload(ctx, env.repo, u.ID); !got.ExpiresAt.Equal(soon) {
		t.Errorf("expiry shortened to %v", got.ExpiresAt)
	}

	if err := env.pg.ExtendPresignedUploadExpiry(ctx, u.ID, soon.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if got, _ := env.pg.GetPresignedUpload(ctx, env.repo, u.ID); !got.ExpiresAt.Equal(soon.Add(time.Hour)) {
		t.Errorf("expiry = %v, want it extended", got.ExpiresAt)
	}

	_ = env.pg.MarkPresignedUploadCompleted(ctx, u.ID, "a")
	_ = env.pg.ExtendPresignedUploadExpiry(ctx, u.ID, soon.Add(48*time.Hour))

	if got, _ := env.pg.GetPresignedUpload(ctx, env.repo, u.ID); !got.ExpiresAt.Equal(soon.Add(time.Hour)) {
		t.Errorf("a completed upload's expiry changed to %v", got.ExpiresAt)
	}
}

func testPresignedExistingIdsDropUnknownAndMalformed(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	u := env.newUpload(t, "alice", time.Now().Add(time.Hour))

	got, err := env.pg.ExistingPresignedUploadIDs(ctx, []string{u.ID, "garbage", "00000000-0000-0000-0000-000000000000"})
	if err != nil || !slices.Equal(got, []string{u.ID}) {
		t.Errorf("Existing = %v, %v; want only %s", got, err, u.ID)
	}
}

func testPresignedThePerUserCapCountsOnly(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	const user = "capped"

	mk := func(expires time.Time) *models.PresignedUpload {
		return &models.PresignedUpload{
			RepositoryID: env.repo,
			Path:         "p",
			SizeBytes:    1,
			SHA256:       env.sha,
			CreatedBy:    user,
			ExpiresAt:    expires,
		}
	}

	a, b := mk(time.Now().Add(time.Hour)), mk(time.Now().Add(time.Hour))
	for _, u := range []*models.PresignedUpload{a, b} {
		if err := env.pg.CreatePresignedUploadCapped(ctx, u, 2); err != nil {
			t.Fatal(err)
		}
	}

	if err := env.pg.CreatePresignedUploadCapped(ctx, mk(time.Now().Add(time.Hour)), 2); !errors.Is(err, ErrPresignedUploadCap) {
		t.Fatalf("third upload = %v, want ErrPresignedUploadCap", err)
	}

	// Another user is not affected by the cap.
	other := mk(time.Now().Add(time.Hour))
	other.CreatedBy = "someone-else"

	if err := env.pg.CreatePresignedUploadCapped(ctx, other, 2); err != nil {
		t.Errorf("another user was capped: %v", err)
	}

	// A completed upload frees a slot.
	_ = env.pg.MarkPresignedUploadCompleted(ctx, a.ID, "x")

	if err := env.pg.CreatePresignedUploadCapped(ctx, mk(time.Now().Add(time.Hour)), 2); err != nil {
		t.Errorf("a completed upload still counted against the cap: %v", err)
	}

	// maxPending <= 0 disables the cap.
	for range 3 {
		if err := env.pg.CreatePresignedUploadCapped(ctx, mk(time.Now().Add(time.Hour)), 0); err != nil {
			t.Errorf("uncapped create = %v", err)
		}
	}
}

func testPresignedConstraintsRejectANegativeSizeAnd(t *testing.T, env presignEnv) {
	t.Helper()

	ctx := context.Background()

	bad := &models.PresignedUpload{
		RepositoryID: env.otherRepo,
		Path:         "p",
		SizeBytes:    -1,
		SHA256:       env.sha,
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := env.pg.CreatePresignedUpload(ctx, bad); err == nil {
		t.Error("a negative size was accepted")
	}

	bad = &models.PresignedUpload{
		RepositoryID: env.otherRepo,
		Path:         "p",
		SizeBytes:    1,
		SHA256:       "NOT-A-DIGEST",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := env.pg.CreatePresignedUpload(ctx, bad); err == nil {
		t.Error("a malformed digest was accepted")
	}
}
