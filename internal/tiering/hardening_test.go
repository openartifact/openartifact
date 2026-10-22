// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
)

// testMetrics builds the tier collectors unregistered (metrics.NewRegistry
// registers on the process-wide registry and can only run once).
func testMetrics() *metrics.Registry {
	return &metrics.Registry{
		TierBlobs:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tier_blobs"}, []string{"tier"}),
		TierBytes:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tier_bytes"}, []string{"tier"}),
		TierReadsTotal:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tier_reads"}, []string{"tier"}),
		TierMovesPending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "tier_pending"}),
	}
}

const (
	testExtraBackend = "44444444-4444-4444-4444-444444444444"
	testExtraTierID  = "55555555-5555-5555-5555-555555555555"
)

// replaceTier swaps a field of one tier in the registry snapshot without a
// database round trip (tests that never trigger a Refresh).
func (r *rig) replaceTier(backend string, mutate func(t *Tier)) {
	r.t.Helper()

	snap := r.reg.Snapshot()
	tiers := make([]*Tier, len(snap.Tiers))

	for i, t := range snap.Tiers {
		c := *t
		tiers[i] = &c

		if c.BackendID == backend {
			mutate(tiers[i])
		}
	}

	r.reg.snap.Store(newSnapshot(tiers))
}

// addExtraTier registers a third tier whose backend config is cfg.
func (r *rig) addExtraTier(kind string, cfg map[string]any) {
	r.t.Helper()

	raw, _ := json.Marshal(cfg)

	r.db.backends = append(r.db.backends, &models.StorageBackend{ID: testExtraBackend, Name: "extra", Kind: kind, Config: raw})
	r.db.tiers = append(r.db.tiers, &models.StorageTier{ID: testExtraTierID, Name: "extra", BackendID: testExtraBackend, BackendName: "extra", BackendKind: kind, Rank: 20})
}

// --- finding 1: aliased backends --------------------------------------------

func TestRegistryRejectsAliasedBackends(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		primary func(r *rig) storage.StorageConfig
		extra   func(r *rig, tmp string) (string, map[string]any)
	}{
		{
			name: "filesystem symlink to the primary directory",
			extra: func(r *rig, tmp string) (string, map[string]any) {
				link := filepath.Join(tmp, "link")
				if err := os.Symlink(r.hotDir, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}

				return "filesystem", map[string]any{"path": link}
			},
		},
		{
			name: "filesystem subdirectory of the primary directory",
			extra: func(r *rig, _ string) (string, map[string]any) {
				return "filesystem", map[string]any{"path": filepath.Join(r.hotDir, "cold")}
			},
		},
		{
			name: "filesystem parent of the primary directory",
			extra: func(r *rig, _ string) (string, map[string]any) {
				return "filesystem", map[string]any{"path": filepath.Dir(r.hotDir)}
			},
		},
		{
			name: "filesystem subdirectory of another tier",
			extra: func(r *rig, _ string) (string, map[string]any) {
				return "filesystem", map[string]any{"path": filepath.Join(r.coldDir, "nested")}
			},
		},
		{
			name: "same inode through a different path spelling",
			extra: func(r *rig, _ string) (string, map[string]any) {
				return "filesystem", map[string]any{"path": r.coldDir + string(filepath.Separator) + "." + string(filepath.Separator)}
			},
		},
		{
			name: "s3 bucket of an s3 primary",
			primary: func(*rig) storage.StorageConfig {
				return storage.StorageConfig{Type: "s3", Endpoint: "minio:9000", Bucket: "artifacts"}
			},
			extra: func(*rig, string) (string, map[string]any) {
				return "garage", map[string]any{"endpoint": "HTTPS://Minio:9000/", "bucket": "artifacts"}
			},
		},
		{
			name:    "gcs bucket of a gcs primary",
			primary: func(*rig) storage.StorageConfig { return storage.StorageConfig{Type: "gcs", GCSBucket: "bkt"} },
			extra: func(*rig, string) (string, map[string]any) {
				return "gcs", map[string]any{"bucket": "bkt"}
			},
		},
		{
			name: "azure container of a primary on the same account",
			primary: func(*rig) storage.StorageConfig {
				return storage.StorageConfig{Type: "azure", AzureAccountName: "Acct", AzureContainerName: "c"}
			},
			extra: func(*rig, string) (string, map[string]any) {
				return "azure", map[string]any{"account": "acct", "container": "c"}
			},
		},
		{
			name: "oci repository of an oci primary",
			primary: func(*rig) storage.StorageConfig {
				return storage.StorageConfig{Type: "oci", Endpoint: "registry.local:5000", Bucket: "blobs"}
			},
			extra: func(*rig, string) (string, map[string]any) {
				return "oci", map[string]any{"endpoint": "https://registry.local:5000", "repository": "blobs"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t)
			kind, cfg := tc.extra(r, t.TempDir())
			r.addExtraTier(kind, cfg)

			primaryCfg := storage.StorageConfig{Type: "filesystem", Path: r.hotDir}
			if tc.primary != nil {
				primaryCfg = tc.primary(r)
			}

			reg := NewRegistry(r.db, r.hot, primaryCfg, nil)
			if err := reg.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}

			extra, ok := reg.Snapshot().ByBackend(testExtraBackend)
			if !ok || extra.Store != nil || !errors.Is(extra.Err, ErrTierUnavailable) || !errors.Is(extra.Err, errAliasedBackend) {
				t.Fatalf("aliasing tier = %+v; want unavailable with errAliasedBackend", extra)
			}

			// The other tiers are unaffected (for a filesystem primary the cold tier is a sibling).
			if tc.primary == nil {
				if cold, _ := reg.Snapshot().ByBackend(testColdBackend); cold == nil || cold.Store == nil {
					t.Errorf("cold tier = %+v, want it open", cold)
				}
			}
		})
	}
}

func TestLocateDistinguishesStorages(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	same := func(a, b storage.StorageConfig) bool { return locate(a).id() == locate(b).id() }

	for _, tc := range []struct {
		name string
		a, b storage.StorageConfig
		want bool
	}{
		{"different buckets", storage.StorageConfig{Type: "s3", Endpoint: "m:9000", Bucket: "a"}, storage.StorageConfig{Type: "s3", Endpoint: "m:9000", Bucket: "b"}, false},
		{"different endpoints", storage.StorageConfig{Type: "s3", Endpoint: "m1:9000", Bucket: "a"}, storage.StorageConfig{Type: "s3", Endpoint: "m2:9000", Bucket: "a"}, false},
		{"s3 family kinds share a bucket", storage.StorageConfig{Type: "s3", Endpoint: "m:9000", Bucket: "a"}, storage.StorageConfig{Type: "seaweedfs", Endpoint: "http://m:9000/", Bucket: "a"}, true},
		{"azure containers", storage.StorageConfig{Type: "azure", AzureAccountName: "x", AzureContainerName: "a"}, storage.StorageConfig{Type: "azure", AzureAccountName: "x", AzureContainerName: "b"}, false},
		{"sibling directories", storage.StorageConfig{Type: "filesystem", Path: filepath.Join(base, "a")}, storage.StorageConfig{Type: "filesystem", Path: filepath.Join(base, "b")}, false},
		{"directories with a common name prefix", storage.StorageConfig{Type: "filesystem", Path: filepath.Join(base, "data")}, storage.StorageConfig{Type: "filesystem", Path: filepath.Join(base, "data-cold")}, false},
	} {
		if got := same(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: same = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Distinct sibling directories do not overlap, even with a shared name prefix.
	cl := newClaims()

	if err := cl.claim("a", location{fsPath: filepath.Join(base, "data")}); err != nil {
		t.Fatal(err)
	}

	if err := cl.claim("b", location{fsPath: filepath.Join(base, "data-cold")}); err != nil {
		t.Errorf("sibling with a shared prefix rejected: %v", err)
	}
}

// Defense in depth: even if a registry let two tiers share storage, the mover
// refuses the move rather than "copying" a blob onto itself and deleting it.
func TestMoveRefusesTiersSharingStorage(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	data := []byte("the only copy")
	sha := r.addBlob(data, 0)

	r.replaceTier(testColdBackend, func(c *Tier) {
		c.Store = r.hot // the "cold" tier is the hot store under another name
		c.identity = r.reg.Snapshot().Primary().identity
	})

	out, err := r.mover.Move(context.Background(), sha, testColdBackend, "test")
	if !errors.Is(err, ErrTierUnavailable) || !errors.Is(err, errSameStorage) || out == OutcomeMoved {
		t.Fatalf("Move = %v, %v; want a refusal", out, err)
	}

	if len(r.db.moves) != 0 {
		t.Errorf("a refused move was journaled: %+v", r.db.moves)
	}

	if ok, _ := r.hot.Exists(context.Background(), sha); !ok {
		t.Fatal("the blob's only copy was deleted")
	}
}

// removeOldCopy re-confirms the target copy under the blob lock: when it is
// gone after the flip, the source is the only copy left and must stay.
func TestRemoveOldCopyKeepsSourceWhenTargetCopyVanished(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()
	data := []byte("target copy disappears after the flip")
	sha := r.addBlob(data, 0)

	r.mover.fault = func(step string) error {
		if step == stepFlipped {
			return r.cold.Delete(ctx, sha)
		}

		return nil
	}

	_, err := r.mover.Move(ctx, sha, testColdBackend, "test")
	if err == nil || !errors.Is(err, storage.ErrBlobNotFound) {
		t.Fatalf("Move = %v, want the missing target copy reported", err)
	}

	if hot, _, _ := r.where(sha); !hot {
		t.Fatal("source copy was deleted although the target copy was missing")
	}
}

// --- finding 4: unusable tiers ----------------------------------------------

func TestMoveDoesNotJournalForUnavailableTier(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	sha := r.addBlob([]byte("target cannot be opened"), 0)

	r.replaceTier(testColdBackend, func(c *Tier) { c.Store, c.Err = nil, ErrTierUnavailable })

	if _, err := r.mover.Move(context.Background(), sha, testColdBackend, "test"); !errors.Is(err, ErrTierUnavailable) {
		t.Fatalf("Move = %v, want ErrTierUnavailable", err)
	}

	if len(r.db.moves) != 0 {
		t.Errorf("an impossible move was journaled: %+v", r.db.moves)
	}
}

func TestEngineSkipsBlobsOnUnopenableTier(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	r.addExtraTier("filesystem", map[string]any{"path": filepath.Join(blocker, "sub")}) // cannot be created

	sha := r.addBlob([]byte("stuck on a tier that cannot be opened"), 48*time.Hour)

	r.db.mu.Lock()
	r.db.blobs[sha].BackendID = testExtraBackend
	r.db.mu.Unlock()

	r.coldPolicy() // "older than a day -> cold" with no from_tier: matches the blob

	st, err := r.engine.RunPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if st.Failed != 0 || len(r.db.moves) != 0 {
		t.Errorf("pass stats %+v, journal %+v; want the blob skipped without a journal row", st, r.db.moves)
	}
}

// --- finding 8: from_tier unknown to this replica ---------------------------

func TestUnknownFromTierPolicyIsDroppedNotDereferenced(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.addBlob([]byte("some blob"), 48*time.Hour)

	r.db.policies = []*models.StoragePolicy{{
		ID: "p1", Name: "from-elsewhere", Enabled: true, Position: 1, TargetTierID: testColdTierID,
		FromTierID: "tier-created-on-another-replica", MinAgeSeconds: new(int64(86400)),
	}}

	if _, err := r.engine.RunPass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err) // a nil dereference used to panic here
	}

	snap := r.reg.Snapshot()
	if got := usablePolicies(snap, mustCompile(t, r.db.policies)); len(got) != 0 {
		t.Errorf("usablePolicies kept a policy with an unknown from_tier: %+v", got)
	}
}

func mustCompile(t *testing.T, rows []*models.StoragePolicy) []*Policy {
	t.Helper()

	out := make([]*Policy, 0, len(rows))

	for _, row := range rows {
		p, err := CompilePolicy(row)
		if err != nil {
			t.Fatal(err)
		}

		out = append(out, p)
	}

	return out
}

// --- finding 9: panic in a mover worker -------------------------------------

func TestEnginePassSurvivesPanickingMove(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.addBlob([]byte("a blob whose move panics"), 48*time.Hour)
	r.coldPolicy()

	r.mover.fault = func(string) error { panic("boom") }

	st, err := r.engine.RunPass(context.Background())
	if err != nil {
		t.Fatalf("pass: %v", err)
	}

	if st.Failed != 1 {
		t.Errorf("stats %+v, want the panicking move counted as failed", st)
	}
}

// --- finding 2/3: read and delete errors ------------------------------------

func TestStoreReadsSurfaceFailuresInsteadOfNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("exists, but its tier is down")

	t.Run("database failure while the blob is not on the primary", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		sha := r.addBlob(data, 0)
		r.moveCold(sha)
		r.restart()

		r.db.mu.Lock()
		r.db.placementErr = errors.New("connection refused")
		r.db.mu.Unlock()

		// The sweep over every tier would still find it; make the cold tier fail too.
		r.replaceTier(testColdBackend, func(c *Tier) { c.Store = failingStore{r.cold} })

		if _, err := r.store.Get(ctx, sha); err == nil || errors.Is(err, storage.ErrBlobNotFound) {
			t.Errorf("Get = %v, want the failure, not a 404", err)
		}

		if _, err := r.store.GetSize(ctx, sha); err == nil || errors.Is(err, storage.ErrBlobNotFound) {
			t.Errorf("GetSize = %v, want the failure, not a 404", err)
		}

		if ok, err := r.store.Exists(ctx, sha); err == nil || ok {
			t.Errorf("Exists = %v, %v; want an error, not (false, nil)", ok, err)
		}
	})

	t.Run("placement on a tier that cannot be opened", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		sha := r.addBlob(data, 0)
		r.moveCold(sha)
		r.restart()
		r.replaceTier(testColdBackend, func(c *Tier) { c.Store, c.Err = nil, ErrTierUnavailable })

		if _, err := r.store.Get(ctx, sha); !errors.Is(err, ErrTierUnavailable) {
			t.Errorf("Get = %v, want ErrTierUnavailable", err)
		}

		if ok, err := r.store.Exists(ctx, sha); !errors.Is(err, ErrTierUnavailable) || ok {
			t.Errorf("Exists = %v, %v; want ErrTierUnavailable", ok, err)
		}
	})

	t.Run("a backend error on a tier met in the sweep", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		sha := r.addBlob(data, 0)

		// The row has no placement (stale), the bytes are nowhere reachable and
		// the cold tier errors: the error must win over "not found".
		r.moveCold(sha)
		r.db.mu.Lock()
		r.db.blobs[sha].BackendID = ""
		r.db.mu.Unlock()
		r.restart()
		r.replaceTier(testColdBackend, func(c *Tier) { c.Store = failingStore{r.cold} })

		if _, err := r.store.Get(ctx, sha); err == nil || errors.Is(err, storage.ErrBlobNotFound) {
			t.Errorf("Get = %v, want the cold tier's failure", err)
		}
	})

	t.Run("a blob that exists nowhere is still not found", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		if _, err := r.store.Get(ctx, "ab"+r.addBlob(data, 0)[2:]); !errors.Is(err, storage.ErrBlobNotFound) {
			t.Errorf("Get(unknown) = %v, want ErrBlobNotFound", err)
		}
	})
}

// failingStore answers every read with a backend failure.
type failingStore struct{ storage.BlobStore }

var errBackendDown = errors.New("backend returned 503")

func (failingStore) Get(context.Context, string) (io.ReadCloser, error) { return nil, errBackendDown }

func (failingStore) GetSize(context.Context, string) (int64, error) { return 0, errBackendDown }

func (failingStore) GetMetadata(context.Context, string) (map[string]string, error) {
	return nil, errBackendDown
}

func (failingStore) Exists(context.Context, string) (bool, error) { return false, errBackendDown }

func TestStoreDeleteKeepsRowWhenPlacementTierIsUnknown(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()
	sha := r.addBlob([]byte("placed on a tier created elsewhere"), 0)

	r.db.mu.Lock()
	r.db.blobs[sha].BackendID = "99999999-9999-9999-9999-999999999999" // not in this replica's registry
	r.db.mu.Unlock()

	if err := r.store.Delete(ctx, sha); !errors.Is(err, ErrTierUnavailable) {
		t.Fatalf("Delete = %v, want ErrTierUnavailable (a nil would let the caller drop the row and orphan the bytes)", err)
	}
}

// --- finding 6: no cold tier -------------------------------------------------

func TestNoColdTierSkipsTrackingAndUsageAggregate(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.db.tiers, r.db.backends = r.db.tiers[:1], nil // only the primary tier remains
	r.restart()

	tracker := NewAccessTracker(r.db, 0, nil)
	m := testMetrics()
	store := NewStore(r.reg, r.db, tracker, m, nil)
	engine := NewEngine(r.db, r.reg, r.mover, EngineConfig{}, m, nil)
	sha := r.addBlob([]byte("plain single-store setup"), 0)

	rc, err := store.Get(storage.WithAccessTracking(context.Background()), sha)
	if err != nil {
		t.Fatal(err)
	}

	_ = rc.Close()

	if tracker.Pending() != 0 {
		t.Errorf("a read without any cold tier was tracked (%d pending)", tracker.Pending())
	}

	if _, err := engine.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}

	if r.db.usageCalls != 0 {
		t.Errorf("StorageTierUsage ran %d times with no cold tier", r.db.usageCalls)
	}
}

func TestUsageAggregateIsCachedBetweenPasses(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	m := testMetrics()
	now := r.now
	engine := NewEngine(r.db, r.reg, r.mover, EngineConfig{Batch: 100}, m, nil)
	engine.now = func() time.Time { return now }

	r.addBlob([]byte("counted once per ttl"), 0)
	ctx := context.Background()

	if _, err := engine.RunPass(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.RunPass(ctx); err != nil {
		t.Fatal(err)
	}

	if r.db.usageCalls != 1 {
		t.Fatalf("usage aggregate ran %d times over two idle passes, want 1", r.db.usageCalls)
	}

	if got := testutil.ToFloat64(m.TierBlobs.WithLabelValues("hot")); got != 1 {
		t.Errorf("hot blobs gauge = %v, want 1", got)
	}

	// Staleness bound.
	now = now.Add(metricsUsageTTL + time.Minute)

	if _, err := engine.RunPass(ctx); err != nil {
		t.Fatal(err)
	}

	if r.db.usageCalls != 2 {
		t.Errorf("usage aggregate not refreshed after the TTL (%d calls)", r.db.usageCalls)
	}

	// A pass that moves a blob re-reads the usage, so the gauges follow it.
	r.addBlob([]byte("old enough to be archived"), 48*time.Hour)
	r.coldPolicy()

	if st, err := engine.RunPass(ctx); err != nil || st.Moved != 1 {
		t.Fatalf("pass = %+v, %v; want one move", st, err)
	}

	if r.db.usageCalls != 3 {
		t.Errorf("usage aggregate calls = %d after a moving pass, want 3", r.db.usageCalls)
	}

	if got := testutil.ToFloat64(m.TierBlobs.WithLabelValues("cold")); got != 1 {
		t.Errorf("cold blobs gauge = %v, want 1", got)
	}
}

func TestMetricsAreUpdatedInPlaceAndDroppedWithTheirTier(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	m := testMetrics()
	engine := NewEngine(r.db, r.reg, r.mover, EngineConfig{Batch: 100}, m, nil)
	engine.now = func() time.Time { return r.now }
	ctx := context.Background()

	r.addBlob([]byte("gauge me"), 0)

	engine.refreshMetrics(ctx, r.reg.Snapshot(), true)

	if n := testutil.CollectAndCount(m.TierBlobs); n != 2 {
		t.Fatalf("series after the first refresh = %d, want 2 (hot, cold)", n)
	}

	// Refresh again: the series must never disappear in between (no Reset).
	engine.refreshMetrics(ctx, r.reg.Snapshot(), true)

	if n := testutil.CollectAndCount(m.TierBlobs); n != 2 {
		t.Errorf("series after a second refresh = %d, want 2", n)
	}

	// Removing the cold tier drops only its series.
	r.db.tiers, r.db.backends = r.db.tiers[:1], nil
	if err := r.reg.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	engine.refreshMetrics(ctx, r.reg.Snapshot(), false)

	if n := testutil.CollectAndCount(m.TierBlobs); n != 0 {
		t.Errorf("series left with no cold tier = %d, want 0", n)
	}
}

// --- finding 11: failed opens are retried -----------------------------------

func TestRegistryRetriesFailedBackendOpen(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()

	blocker := filepath.Join(t.TempDir(), "mount")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil { // "the mount is not there yet"
		t.Fatal(err)
	}

	r.addExtraTier("filesystem", map[string]any{"path": filepath.Join(blocker, "tier")})

	if err := r.reg.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	if extra, _ := r.reg.Snapshot().ByBackend(testExtraBackend); extra == nil || extra.Store != nil {
		t.Fatalf("tier opened although its path cannot be created: %+v", extra)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(blocker, 0o750); err != nil { // the mount appears
		t.Fatal(err)
	}

	if err := r.reg.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	if extra, _ := r.reg.Snapshot().ByBackend(testExtraBackend); extra == nil || extra.Store == nil || extra.Err != nil {
		t.Errorf("tier did not recover on the next refresh: %+v", extra)
	}
}

// --- finding 7: list dedup signal -------------------------------------------

func TestStoreListMayRepeatOnlyWithColdTier(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	if !r.store.ListMayRepeat() {
		t.Error("ListMayRepeat = false with a cold tier")
	}

	r.db.tiers, r.db.backends = r.db.tiers[:1], nil
	r.restart()

	if r.store.ListMayRepeat() {
		t.Error("ListMayRepeat = true with only the primary tier")
	}
}
