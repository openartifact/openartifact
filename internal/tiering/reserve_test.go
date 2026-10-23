// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/storage"
)

func TestCheckDistinct(t *testing.T) {
	t.Parallel()

	fs := func(p string) storage.StorageConfig { return storage.StorageConfig{Type: "filesystem", Path: p} }
	s3 := func(bucket string) storage.StorageConfig {
		return storage.StorageConfig{Type: "s3", Endpoint: "https://s3.example.com", Bucket: bucket}
	}

	tests := []struct {
		name    string
		a, b    storage.StorageConfig
		wantErr bool
	}{
		{"different directories", fs("/data/a"), fs("/data/b"), false},
		{"same directory", fs("/data/a"), fs("/data/a/"), true},
		{"nested directory", fs("/data/a"), fs("/data/a/b"), true},
		{"different buckets", s3("one"), s3("two"), false},
		{"same bucket", s3("one"), s3("one"), true},
		{"filesystem and bucket", fs("/data/a"), s3("one"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := CheckDistinct(tc.a, tc.b); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestReservedLocationIsRefusedForTiers(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()

	// The replica lives where the cold tier's backend points.
	r.reg.Reserve(storage.StorageConfig{Type: "filesystem", Path: r.coldDir})

	if err := r.reg.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	cold, ok := r.reg.Snapshot().ByBackend(testColdBackend)
	if !ok || cold.Store != nil || !errors.Is(cold.Err, ErrTierUnavailable) {
		t.Fatalf("cold tier = %+v; want unavailable (its path is the replica's)", cold)
	}

	if r.reg.Snapshot().Primary().Store == nil {
		t.Fatal("the primary tier became unavailable")
	}
}
