// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/openartifact/openartifact/internal/metrics"
)

func histogramCount(t *testing.T, reg *metrics.Registry, operation, result string) uint64 {
	t.Helper()

	obs, err := reg.BlobOperationDurationSeconds.GetMetricWithLabelValues(operation, result)
	if err != nil {
		t.Fatal(err)
	}

	var m dto.Metric
	if err := obs.(interface{ Write(*dto.Metric) error }).Write(&m); err != nil {
		t.Fatal(err)
	}

	return m.GetHistogram().GetSampleCount()
}

func TestMetricsBlobStoreRecordsOperationLatency(t *testing.T) {
	inner, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	reg := metrics.NewRegistryOn(prometheus.NewRegistry())
	store := NewMetricsBlobStore(inner, reg)
	ctx := context.Background()

	body := []byte("hello blob")
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:])

	if _, err := store.Put(ctx, bytes.NewReader(body), map[string]string{"sha256": id}); err != nil {
		t.Fatal(err)
	}

	rc, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}

	_ = rc.Close()

	if _, err := store.Exists(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetSize(ctx, id); err != nil {
		t.Fatal(err)
	}

	// A digest that does not match the bytes fails the put and is counted as an error.
	if _, err := store.Put(ctx, bytes.NewReader(body), map[string]string{"sha256": hex.EncodeToString(make([]byte, 32))}); err == nil {
		t.Fatal("Put with a wrong digest succeeded")
	}

	for _, tc := range []struct {
		operation, result string
		want              uint64
	}{
		{"put", "ok", 1},
		{"put", "error", 1},
		{"get", "ok", 1},
		{"exists", "ok", 1},
		{"size", "ok", 1},
		{"delete", "ok", 0},
	} {
		if got := histogramCount(t, reg, tc.operation, tc.result); got != tc.want {
			t.Errorf("%s/%s samples = %d, want %d", tc.operation, tc.result, got, tc.want)
		}
	}
}
