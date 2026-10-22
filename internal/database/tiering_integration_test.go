// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

type tierFixture struct {
	pg   *PostgresDB
	cold string // storage backend id of the cold tier
}

func newTierFixture(t *testing.T) *tierFixture {
	t.Helper()

	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	now := time.Now()

	b := &models.StorageBackend{
		ID: uuid.New().String(), Name: "cold-fs", Kind: "filesystem", Config: []byte(`{"path":"/tmp/cold"}`),
		Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}
	if err := pg.CreateStorageBackend(ctx, b); err != nil {
		t.Fatalf("CreateStorageBackend: %v", err)
	}

	if err := pg.CreateStorageTier(ctx, &models.StorageTier{ID: uuid.New().String(), Name: "cold", BackendID: b.ID, Rank: 100}); err != nil {
		t.Fatalf("CreateStorageTier: %v", err)
	}

	return &tierFixture{pg: pg, cold: b.ID}
}

func (f *tierFixture) addBlob(t *testing.T, name string, size int64, age time.Duration) string {
	t.Helper()

	sum := sha256.Sum256([]byte(name))
	sha := hex.EncodeToString(sum[:])

	if _, err := f.pg.GetDB().ExecContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key, created_at) VALUES ($1::text, $2, $1::text, now() - make_interval(secs => $3::double precision))`,
		sha, size, age.Seconds()); err != nil {
		t.Fatalf("insert blob: %v", err)
	}

	return sha
}

func TestTieringMigrationSeedsPrimaryTier(t *testing.T) {
	pg := newQuotaTestDB(t, 4)

	tiers, err := pg.ListStorageTiers(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(tiers) != 1 || tiers[0].Name != "hot" || tiers[0].BackendID != "" {
		t.Fatalf("seeded tiers = %+v, want exactly the primary tier hot", tiers)
	}

	var n int
	if err := pg.GetDB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_blobs_tier_promote'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("idx_blobs_tier_promote present = %d (%v)", n, err)
	}

	// The superseded index of migration 032 is gone.
	if err := pg.GetDB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_blobs_tier_reads'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("idx_blobs_tier_reads present = %d (%v), want it dropped by migration 034", n, err)
	}
}

func TestTieringTierAndPolicyConstraints(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()

	// The same backend cannot be two tiers.
	err := f.pg.CreateStorageTier(ctx, &models.StorageTier{ID: uuid.New().String(), Name: "again", BackendID: f.cold, Rank: 5})
	if UniqueViolationConstraint(err) != "idx_storage_tiers_backend" {
		t.Errorf("duplicate backend tier = %v, want idx_storage_tiers_backend violation", err)
	}

	// A backend that is a tier cannot be deleted.
	if err := f.pg.DeleteStorageBackend(ctx, f.cold); !IsForeignKeyViolation(err) {
		t.Errorf("DeleteStorageBackend(tier backend) = %v, want a foreign-key violation", err)
	}

	cold, err := f.pg.GetStorageTierByName(ctx, "cold")
	if err != nil || cold == nil {
		t.Fatalf("GetStorageTierByName = %v, %v", cold, err)
	}

	pol := &models.StoragePolicy{
		ID: uuid.New().String(), Name: "archive", Enabled: true, Position: 1, TargetTierID: cold.ID,
		MinAgeSeconds: new(int64(86400)), RepoGlob: "rel*", Formats: []string{"maven", "npm"},
	}
	if err := f.pg.CreateStoragePolicy(ctx, pol); err != nil {
		t.Fatalf("CreateStoragePolicy: %v", err)
	}

	got, err := f.pg.GetStoragePolicy(ctx, pol.ID)
	if err != nil || got == nil || got.RepoGlob != "rel*" || len(got.Formats) != 2 || got.MinAgeSeconds == nil || *got.MinAgeSeconds != 86400 {
		t.Fatalf("policy round trip = %+v, %v", got, err)
	}

	// A tier named by a policy cannot be deleted either.
	if reason, err := f.pg.DeleteStorageTierIfUnused(ctx, cold.ID); err != nil || !strings.Contains(reason, "policy") {
		t.Errorf("DeleteStorageTierIfUnused(policy) = %q, %v, want the policy reason", reason, err)
	}

	if ok, err := f.pg.DeleteStoragePolicy(ctx, pol.ID); err != nil || !ok {
		t.Fatalf("DeleteStoragePolicy = %v, %v", ok, err)
	}

	if reason, err := f.pg.DeleteStorageTierIfUnused(ctx, cold.ID); err != nil || reason != "" {
		t.Errorf("DeleteStorageTierIfUnused = %q, %v", reason, err)
	}

	if got, _ := f.pg.GetStorageTierByName(ctx, "cold"); got != nil {
		t.Errorf("tier still present after delete: %+v", got)
	}
}

// TestDeleteStorageTierIfUnusedRefusesBlobsAndMoves covers the blob and journal
// guards; the blobs foreign key is RESTRICT, so a backend holding blobs can
// never be deleted out from under them either.
func TestDeleteStorageTierIfUnusedRefusesBlobsAndMoves(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()
	sha := f.addBlob(t, "held", 10, time.Hour)

	if _, _, err := f.pg.BeginStorageTierMove(ctx, &models.StorageTierMove{SHA256: sha, FromBackendID: "", ToBackendID: f.cold, SizeBytes: 10, Reason: "t"}); err != nil {
		t.Fatalf("BeginStorageTierMove: %v", err)
	}

	cold, _ := f.pg.GetStorageTierByName(ctx, "cold")
	if reason, err := f.pg.DeleteStorageTierIfUnused(ctx, cold.ID); err != nil || !strings.Contains(reason, "move") {
		t.Fatalf("with a journaled move = %q, %v", reason, err)
	}

	if _, err := f.pg.GetDB().ExecContext(ctx, `DELETE FROM storage_tier_moves`); err != nil {
		t.Fatal(err)
	}

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE blobs SET storage_backend_id = $1 WHERE sha256 = $2`, f.cold, sha); err != nil {
		t.Fatal(err)
	}

	if reason, err := f.pg.DeleteStorageTierIfUnused(ctx, cold.ID); err != nil || !strings.Contains(reason, "blobs") {
		t.Fatalf("with blobs = %q, %v", reason, err)
	}

	if reason, err := f.pg.StorageBackendBusyReason(ctx, f.cold); err != nil || reason == "" {
		t.Errorf("StorageBackendBusyReason = %q, %v, want busy", reason, err)
	}

	// RESTRICT: deleting the backend must fail while blobs sit on it.
	if err := f.pg.DeleteStorageBackend(ctx, f.cold); !IsForeignKeyViolation(err) {
		t.Errorf("DeleteStorageBackend(holding blobs) = %v, want a foreign-key violation", err)
	}
}

func TestFlipBlobPlacement(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()
	sha := f.addBlob(t, "flip", 10, time.Hour)

	if _, _, err := f.pg.BeginStorageTierMove(ctx, &models.StorageTierMove{SHA256: sha, FromBackendID: "", ToBackendID: f.cold, SizeBytes: 10, Reason: "t"}); err != nil {
		t.Fatalf("BeginStorageTierMove: %v", err)
	}

	verifyErr := errors.New("target copy vanished")

	// A failing verify aborts before any change.
	if _, err := f.pg.FlipBlobPlacement(ctx, sha, "", f.cold, func(context.Context) error { return verifyErr }); !errors.Is(err, verifyErr) {
		t.Fatalf("flip with failing verify = %v", err)
	}

	if be, _, _, _ := f.pg.GetBlobPlacement(ctx, sha); be != "" {
		t.Fatalf("placement changed to %q despite failed verify", be)
	}

	// Wrong expected source: nothing changes.
	if out, err := f.pg.FlipBlobPlacement(ctx, sha, f.cold, "", nil); err != nil || out != models.FlipPlacementChanged {
		t.Fatalf("flip from the wrong source = %v, %v", out, err)
	}

	if out, err := f.pg.FlipBlobPlacement(ctx, sha, "", f.cold, nil); err != nil || out != models.FlipDone {
		t.Fatalf("flip = %v, %v", out, err)
	}

	if be, size, ok, _ := f.pg.GetBlobPlacement(ctx, sha); be != f.cold || size != 10 || !ok {
		t.Fatalf("placement after flip = %q %d %v", be, size, ok)
	}

	moves, err := f.pg.ListStorageTierMoves(ctx, 10)
	if err != nil || len(moves) != 1 || moves[0].State != models.TierMoveFlipped {
		t.Fatalf("journal after flip = %+v, %v; want state flipped", moves, err)
	}

	var changed *time.Time
	if err := f.pg.GetDB().QueryRowContext(ctx, `SELECT tier_changed_at FROM blobs WHERE sha256 = $1`, sha).Scan(&changed); err != nil || changed == nil {
		t.Errorf("tier_changed_at = %v (%v), want set", changed, err)
	}

	// A flip is idempotent in outcome: repeating it reports the changed placement.
	if out, _ := f.pg.FlipBlobPlacement(ctx, sha, "", f.cold, nil); out != models.FlipPlacementChanged {
		t.Errorf("repeated flip = %v, want placement changed", out)
	}

	// Unknown blob.
	if out, err := f.pg.FlipBlobPlacement(ctx, "00"+sha[2:], "", f.cold, nil); err != nil || out != models.FlipBlobGone {
		t.Errorf("flip of a missing blob = %v, %v", out, err)
	}
}

// The flip takes the same per-blob advisory lock as uploaders and
// DeleteBlobIfUnreferenced: it waits for a holder and never overlaps it.
func TestFlipBlobPlacementWaitsForBlobLock(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()
	sha := f.addBlob(t, "lock", 10, time.Hour)

	held := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)

	go func() {
		lockDone <- f.pg.WithBlobLock(ctx, sha, func(context.Context) error {
			close(held)
			<-release

			return nil
		})
	}()

	<-held

	flipped := make(chan models.FlipOutcome, 1)

	go func() {
		out, _ := f.pg.FlipBlobPlacement(ctx, sha, "", f.cold, nil)
		flipped <- out
	}()

	select {
	case out := <-flipped:
		t.Fatalf("flip completed (%v) while another session held the blob lock", out)
	case <-time.After(300 * time.Millisecond):
	}

	if be, _, _, _ := f.pg.GetBlobPlacement(ctx, sha); be != "" {
		t.Fatalf("placement changed to %q under a held blob lock", be)
	}

	close(release)

	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}

	select {
	case out := <-flipped:
		if out != models.FlipDone {
			t.Errorf("flip = %v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flip did not proceed after the lock was released")
	}
}

func TestStorageTierMoveJournal(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()
	sha := f.addBlob(t, "journal", 10, time.Hour)

	row, created, err := f.pg.BeginStorageTierMove(ctx, &models.StorageTierMove{SHA256: sha, ToBackendID: f.cold, SizeBytes: 10, Reason: "a"})
	if err != nil || !created || row.State != models.TierMoveCopying {
		t.Fatalf("Begin = %+v, %v, %v", row, created, err)
	}

	// A second claim returns the existing row and does not create another.
	row2, created, err := f.pg.BeginStorageTierMove(ctx, &models.StorageTierMove{SHA256: sha, FromBackendID: f.cold, ToBackendID: "", SizeBytes: 10, Reason: "b"})
	if err != nil || created || row2.ToBackendID != f.cold {
		t.Fatalf("second Begin = %+v, %v, %v; want the first row back", row2, created, err)
	}

	if n, _ := f.pg.CountStorageTierMoves(ctx); n != 1 {
		t.Errorf("CountStorageTierMoves = %d, want 1", n)
	}

	// A failed move is retried while it has attempts left, and once it is
	// out of attempts only after the back-off has elapsed.
	if err := f.pg.FailStorageTierMove(ctx, sha, "boom"); err != nil {
		t.Fatal(err)
	}

	rec, err := f.pg.ListRecoverableStorageTierMoves(ctx, 5, time.Hour, 10)
	if err != nil || len(rec) != 1 || rec[0].Attempts != 1 || rec[0].LastError != "boom" {
		t.Fatalf("recoverable with attempts left = %+v, %v", rec, err)
	}

	if rec, _ := f.pg.ListRecoverableStorageTierMoves(ctx, 1, time.Hour, 10); len(rec) != 0 {
		t.Errorf("an exhausted move is recoverable inside its back-off: %+v", rec)
	}

	if rec, _ := f.pg.ListRecoverableStorageTierMoves(ctx, 1, 0, 10); len(rec) != 1 {
		t.Errorf("an exhausted move is not recoverable after its back-off: %+v", rec)
	}

	if err := f.pg.FinishStorageTierMove(ctx, sha); err != nil {
		t.Fatal(err)
	}

	if n, _ := f.pg.CountStorageTierMoves(ctx); n != 0 {
		t.Errorf("journal row survived Finish")
	}
}

func TestScanTierCandidatesAndPromotion(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()

	oldBig := f.addBlob(t, "old-big", 5000, 40*24*time.Hour)
	oldSmall := f.addBlob(t, "old-small", 10, 40*24*time.Hour)
	young := f.addBlob(t, "young", 5000, time.Hour)
	onCold := f.addBlob(t, "already-cold", 5000, 40*24*time.Hour)

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE blobs SET storage_backend_id = $1::uuid, tier_changed_at = now() - interval '1 day' WHERE sha256 = $2`, f.cold, onCold); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	minSize := int64(1000)

	clause := models.TierScanClause{TargetBackendID: f.cold, CreatedBefore: &cutoff, MinSizeBytes: &minSize}

	found, next, scanned, err := f.pg.ScanTierCandidates(ctx, "", 100, []models.TierScanClause{clause})
	if err != nil {
		t.Fatalf("ScanTierCandidates: %v", err)
	}

	if len(found) != 1 || found[0].SHA256 != oldBig || next != "" || scanned != 4 {
		t.Errorf("found=%+v next=%q scanned=%d; want only %s (old, big, not yet cold)", found, next, scanned, oldBig[:8])
	}

	// An in-flight move hides the blob from the scan.
	if _, _, err := f.pg.BeginStorageTierMove(ctx, &models.StorageTierMove{SHA256: oldBig, ToBackendID: f.cold, SizeBytes: 5000}); err != nil {
		t.Fatal(err)
	}

	if found, _, _, _ = f.pg.ScanTierCandidates(ctx, "", 100, []models.TierScanClause{clause}); len(found) != 0 {
		t.Errorf("a blob with a move in flight was offered again: %+v", found)
	}

	// Windows page through the table by sha256.
	var seen int

	cursor := ""

	for range 10 {
		_, n, s, err := f.pg.ScanTierCandidates(ctx, cursor, 2, []models.TierScanClause{clause})
		if err != nil {
			t.Fatal(err)
		}

		seen += s

		if cursor = n; cursor == "" {
			break
		}
	}

	if seen != 4 {
		t.Errorf("paged scan covered %d rows, want 4", seen)
	}

	_, _ = oldSmall, young

	// Promotion candidates: read after landing on the cold tier.
	if cand, _ := f.pg.ListPromotionCandidates(ctx, []string{f.cold}, "", 10); len(cand) != 0 {
		t.Fatalf("unread cold blob listed for promotion: %+v", cand)
	}

	if _, err := f.pg.TouchBlobAccess(ctx, map[string]time.Time{onCold: time.Now()}, time.Hour); err != nil {
		t.Fatal(err)
	}

	cand, err := f.pg.ListPromotionCandidates(ctx, []string{f.cold}, "", 10)
	if err != nil || len(cand) != 1 || cand[0].SHA256 != onCold {
		t.Fatalf("promotion candidates = %+v, %v", cand, err)
	}
}

func TestTouchBlobAccessResolution(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()
	sha := f.addBlob(t, "touch", 1, time.Hour)

	base := time.Now().Add(-time.Minute)

	if n, err := f.pg.TouchBlobAccess(ctx, map[string]time.Time{sha: base}, time.Hour); err != nil || n != 1 {
		t.Fatalf("first touch = %d, %v", n, err)
	}

	// Within the resolution window the row is left alone (no write amplification).
	if n, _ := f.pg.TouchBlobAccess(ctx, map[string]time.Time{sha: base.Add(10 * time.Second)}, time.Hour); n != 0 {
		t.Errorf("touch inside the resolution rewrote %d rows", n)
	}

	if n, _ := f.pg.TouchBlobAccess(ctx, map[string]time.Time{sha: base.Add(2 * time.Hour)}, time.Hour); n != 1 {
		t.Errorf("touch past the resolution updated %d rows, want 1", n)
	}

	if n, err := f.pg.TouchBlobAccess(ctx, map[string]time.Time{"ff" + sha[2:]: time.Now()}, time.Hour); err != nil || n != 0 {
		t.Errorf("touch of an unknown blob = %d, %v", n, err)
	}
}

func TestListBlobRefsAndUsage(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()

	s := newQuotaSeeder(t, f.pg, "tier-helm", "helm")
	blobID := s.blob(123)

	var sha string
	if err := f.pg.GetDB().QueryRowContext(ctx, `SELECT sha256 FROM blobs WHERE id = $1`, blobID).Scan(&sha); err != nil {
		t.Fatal(err)
	}

	s.helm(blobID)

	refs, err := f.pg.ListBlobRefs(ctx, []string{sha, "ee" + sha[2:]})
	if err != nil {
		t.Fatalf("ListBlobRefs: %v", err)
	}

	got := refs[sha]
	if len(got) != 1 || got[0].Repo != "tier-helm" || got[0].Format != "helm" || got[0].Path == "" {
		t.Errorf("refs = %+v", got)
	}

	if len(refs["ee"+sha[2:]]) != 0 {
		t.Errorf("unknown blob has refs: %v", refs)
	}

	usage, err := f.pg.StorageTierUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var total int64

	for _, u := range usage {
		total += u.Blobs
	}

	if total != 1 || usage[0].BackendID != "" {
		t.Errorf("usage = %+v, want one blob on the primary store", usage)
	}
}
