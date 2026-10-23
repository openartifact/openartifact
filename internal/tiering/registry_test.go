// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
)

func TestConfigFromBackend(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		kind    string
		raw     string
		wantErr bool
		check   func(t *testing.T, c storage.StorageConfig)
	}{
		{"filesystem", "filesystem", `{"path":"/mnt/hdd"}`, false, func(t *testing.T, c storage.StorageConfig) {
			t.Helper()

			if c.Type != "filesystem" || c.Path != "/mnt/hdd" {
				t.Errorf("config = %+v", c)
			}
		}},
		{"filesystem without path", "filesystem", `{}`, true, nil},
		{"s3 with endpoint uses path style", "s3", `{"endpoint":"http://minio:9000","bucket":"b","region":"us-east-1"}`, false, func(t *testing.T, c storage.StorageConfig) {
			t.Helper()

			if c.Bucket != "b" || c.Endpoint != "http://minio:9000" || !c.ForcePathStyle {
				t.Errorf("config = %+v", c)
			}
		}},
		{"s3 snake_case path style flag", "s3", `{"bucket":"b","force_path_style":true}`, false, func(t *testing.T, c storage.StorageConfig) {
			t.Helper()

			if !c.ForcePathStyle {
				t.Errorf("config = %+v", c)
			}
		}},
		{"gcs", "gcs", `{"bucket":"g"}`, false, func(t *testing.T, c storage.StorageConfig) {
			t.Helper()

			if c.GCSBucket != "g" {
				t.Errorf("config = %+v", c)
			}
		}},
		{"azure", "azure", `{"account":"acct","container":"c"}`, false, func(t *testing.T, c storage.StorageConfig) {
			t.Helper()

			if c.AzureAccountName != "acct" || c.AzureContainerName != "c" {
				t.Errorf("config = %+v", c)
			}
		}},
		{"azure endpoint", "azure", `{"account":"devstoreaccount1","container":"c","endpoint":"http://azurite:10000/devstoreaccount1"}`, false, func(t *testing.T, c storage.StorageConfig) {
			t.Helper()

			if c.AzureEndpoint != "http://azurite:10000/devstoreaccount1" {
				t.Errorf("config = %+v", c)
			}
		}},
		{"oci host gets a scheme", "oci", `{"endpoint":"registry.local:5000","insecure":true,"repository":"r"}`, false, func(t *testing.T, c storage.StorageConfig) {
			t.Helper()

			if c.Endpoint != "http://registry.local:5000" || c.Bucket != "r" {
				t.Errorf("config = %+v", c)
			}
		}},
		{"unsupported kind", "ftp", `{}`, true, nil},
		{"not json", "filesystem", `[1,2]`, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, err := ConfigFromBackend(tc.kind, []byte(tc.raw))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}

			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

func TestRegistryTierAvailability(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()

	cfg := func(p string) []byte {
		b, _ := json.Marshal(map[string]string{"path": p})

		return b
	}

	const (
		aliasBackend = "44444444-4444-4444-4444-444444444444"
		dupBackend   = "55555555-5555-5555-5555-555555555555"
	)

	r.db.backends = append(r.db.backends,
		&models.StorageBackend{ID: aliasBackend, Name: "alias", Kind: "filesystem", Config: cfg(r.hotDir)},
		&models.StorageBackend{ID: dupBackend, Name: "dup", Kind: "filesystem", Config: cfg(r.coldDir)},
	)
	r.db.tiers = append(r.db.tiers,
		&models.StorageTier{ID: "t-alias", Name: "alias", BackendID: aliasBackend, Rank: 20},
		&models.StorageTier{ID: "t-dup", Name: "dup", BackendID: dupBackend, Rank: 30},
	)

	if err := r.reg.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	snap := r.reg.Snapshot()

	for _, name := range []string{"alias", "dup"} {
		var found *Tier

		for _, tier := range snap.Tiers {
			if tier.Name == name {
				found = tier
			}
		}

		if found == nil || found.Store != nil || !errors.Is(found.Err, ErrTierUnavailable) {
			t.Errorf("tier %q = %+v; want unavailable (path shared with another store)", name, found)
		}
	}

	// The healthy tiers are unaffected.
	if cold, ok := snap.ByBackend(testColdBackend); !ok || cold.Store == nil || cold.Err != nil {
		t.Errorf("cold tier = %+v", cold)
	}

	if !snap.Primary().Primary() || snap.Primary().Store == nil {
		t.Errorf("primary = %+v", snap.Primary())
	}
}

func TestRegistryWithoutRowsKeepsPrimaryReachable(t *testing.T) {
	t.Parallel()

	db := newFakeDB() // no tier rows: a database that predates the migration
	dir := filepath.Join(t.TempDir(), "p")

	primary, err := storage.NewBlobStore(storage.StorageConfig{Type: "filesystem", Path: dir})
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(db, primary, storage.StorageConfig{Type: "filesystem", Path: dir}, nil)
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	snap := reg.Snapshot()
	if snap.Primary() == nil || snap.Primary().Store == nil || snap.HasCold() {
		t.Errorf("snapshot = %+v", snap)
	}

	if _, err := reg.PlacementTier("no-such-backend"); !errors.Is(err, ErrUnknownTier) {
		t.Errorf("PlacementTier(unknown) = %v, want ErrUnknownTier", err)
	}
}

// tiersOutageDB fails the tier list, as an unreachable database does.
type tiersOutageDB struct {
	*fakeDB

	err error
}

func (d *tiersOutageDB) ListStorageTiers(ctx context.Context) ([]*models.StorageTier, error) {
	if d.err != nil {
		return nil, d.err
	}

	return d.fakeDB.ListStorageTiers(ctx)
}

// A replica must know its tiers before it takes writes: the first load is
// synchronous and its failure is an error, not a log line, and Ready stays
// false until a load has succeeded.
func TestStartRefusesWithoutTierLoad(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	outage := errors.New("database down")
	db := &tiersOutageDB{fakeDB: r.db, err: outage}

	reg := NewRegistry(db, r.hot, storage.StorageConfig{Type: "filesystem", Path: r.hotDir}, nil)
	if reg.Ready() {
		t.Fatal("Ready before any load")
	}

	if err := reg.Start(context.Background(), 0); !errors.Is(err, outage) {
		t.Fatalf("Start = %v, want the load error", err)
	}

	if reg.Ready() {
		t.Fatal("Ready after a failed first load")
	}

	db.err = nil

	reg = NewRegistry(db, r.hot, storage.StorageConfig{Type: "filesystem", Path: r.hotDir}, nil)
	if err := reg.Start(context.Background(), 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !reg.Ready() {
		t.Fatal("not Ready after a successful load")
	}
}

// With periodic reloads, a replica whose reloads keep failing stops being
// ready after three intervals instead of writing with a stale tier list.
func TestReadyExpiresWhenReloadsFail(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	reg := NewRegistry(r.db, r.hot, storage.StorageConfig{Type: "filesystem", Path: r.hotDir}, nil)

	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	reg.interval.Store(int64(time.Minute))
	reg.lastLoad.Store(time.Now().Add(-2 * time.Minute).UnixNano())

	if !reg.Ready() {
		t.Fatal("not Ready two intervals after the last load")
	}

	reg.lastLoad.Store(time.Now().Add(-4 * time.Minute).UnixNano())

	if reg.Ready() {
		t.Fatal("Ready four intervals after the last load")
	}
}
