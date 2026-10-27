// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/metrics"
)

func newMetricsStore(inner BlobStore) (*MetricsBlobStore, *metrics.Registry) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	return NewMetricsBlobStore(inner, reg), reg
}

// The wrapper is transparent: a relay-staged upload, its promotion and every
// read go through it with the inner store's results, and the bytes read are
// counted.
func TestMetricsBlobStoreForwardsStagingAndReads(t *testing.T) {
	t.Parallel()

	_, fsys := newFSRelayEnv(t)
	store, reg := newMetricsStore(fsys)
	ctx := context.Background()

	if !store.StagingSupported() || !store.MultipartSupported() {
		t.Fatal("wrapper hides the staging support of the inner store")
	}

	data := []byte("staged through the metrics wrapper")
	sha := sha256Of(data)

	put, err := store.PresignStagingPut(ctx, relayTestID, int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	mustStatus(t, relayPut(t, put.URL, data), http.StatusOK)

	obj, err := store.StatStaging(ctx, relayTestID)
	if err != nil || obj.Size != int64(len(data)) {
		t.Fatalf("StatStaging = %+v, %v", obj, err)
	}

	rc, err := store.OpenStaging(ctx, relayTestID, obj.Version)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, data) {
		t.Fatalf("OpenStaging = %q", got)
	}

	if got := testutil.ToFloat64(reg.BlobBytesTotal.WithLabelValues("get")); got != float64(len(data)) {
		t.Fatalf("bytes counted from staging reads = %v, want %d", got, len(data))
	}

	var walked []string

	if err := store.WalkStaging(ctx, fsys.now().Add(time.Hour), func(ids []string) error {
		walked = append(walked, ids...)

		return nil
	}); err != nil || len(walked) != 1 || walked[0] != relayTestID {
		t.Fatalf("WalkStaging = %v, %v", walked, err)
	}

	if err := store.PromoteStaging(ctx, relayTestID, sha, obj.Version, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteStaging(ctx, relayTestID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.StatStaging(ctx, relayTestID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("staging object survived DeleteStaging: %v", err)
	}

	checkWrapperReads(t, store, reg, sha, data)
}

func checkWrapperReads(t *testing.T, store *MetricsBlobStore, reg *metrics.Registry, sha string, data []byte) {
	t.Helper()

	ctx := context.Background()

	md, err := store.GetMetadata(ctx, sha)
	if err != nil || md["k"] != "v" {
		t.Fatalf("GetMetadata = %v, %v", md, err)
	}

	rc, err := store.GetFrom(ctx, sha, 7)
	if err != nil {
		t.Fatal(err)
	}

	if got := string(readAll(t, rc)); got != string(data[7:]) {
		t.Fatalf("GetFrom = %q", got)
	}

	ids, _, err := store.List(ctx, "", 10)
	if err != nil || len(ids) != 1 || ids[0] != sha {
		t.Fatalf("List = %v, %v", ids, err)
	}

	if key, ok, err := store.ExistsBySHA256(ctx, sha); err != nil || !ok || key == "" {
		t.Fatalf("ExistsBySHA256 = %q, %v, %v", key, ok, err)
	}

	if err := store.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}

	if ok, err := store.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("Exists after Delete = %v, %v", ok, err)
	}

	for _, op := range []string{"metadata", "get", "exists_by_sha256", "delete", "exists"} {
		if histogramCount(t, reg, op, "ok") == 0 {
			t.Errorf("no %s latency sample recorded", op)
		}
	}
}

func TestMetricsBlobStoreForwardsMultipart(t *testing.T) {
	t.Parallel()

	env, fsys := newFSRelayEnv(t)
	store, _ := newMetricsStore(fsys)
	ctx := context.Background()

	const partSize = MinPartSize

	data := bytes.Repeat([]byte("0123456789"), int(partSize+500)/10+1)[:partSize+500]
	size := int64(len(data))

	handle, err := store.InitiateMultipart(ctx, relayTestID, size, partSize)
	if err != nil {
		t.Fatal(err)
	}

	for n := 1; n <= PartCount(size, partSize); n++ {
		p, err := store.PresignStagingPart(ctx, relayTestID, handle, n, PartLength(size, partSize, n), time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		start := int64(n-1) * partSize
		mustStatus(t, relayPut(t, p.URL, data[start:start+PartLength(size, partSize, n)]), http.StatusOK)
	}

	if err := store.CompleteMultipart(ctx, relayTestID, handle, size, partSize); err != nil {
		t.Fatal(err)
	}

	if obj, err := env.store.StatStaging(ctx, relayTestID); err != nil || obj.Size != size {
		t.Fatalf("assembled object = %+v, %v", obj, err)
	}
}

// bareStore hides everything but the BlobStore methods of the store it wraps.
type bareStore struct{ BlobStore }

// Over a store with no staging, presigning or multipart the wrapper reports
// that instead of pretending, and reads are refused for the same reason.
func TestMetricsBlobStoreOverPlainStoreReportsUnsupported(t *testing.T) {
	t.Parallel()

	inner, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	store, _ := newMetricsStore(bareStore{inner})
	ctx := context.Background()
	sha := sha256Of([]byte("x"))

	if store.StagingSupported() || store.MultipartSupported() {
		t.Error("plain store reported staging support")
	}

	if _, err := store.PresignURL(ctx, sha, time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PresignURL = %v", err)
	}

	if _, err := store.PresignGet(ctx, sha, time.Minute, PresignGetOptions{}); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PresignGet = %v", err)
	}

	if _, err := store.PresignStagingPut(ctx, relayTestID, 1, time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PresignStagingPut = %v", err)
	}

	if _, err := store.StatStaging(ctx, relayTestID); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("StatStaging = %v", err)
	}

	if _, err := store.OpenStaging(ctx, relayTestID, "v"); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("OpenStaging = %v", err)
	}

	if err := store.PromoteStaging(ctx, relayTestID, sha, "v", nil); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PromoteStaging = %v", err)
	}

	if err := store.DeleteStaging(ctx, relayTestID); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("DeleteStaging = %v", err)
	}

	if err := store.WalkStaging(ctx, time.Now(), func([]string) error { return nil }); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("WalkStaging = %v", err)
	}

	if _, err := store.InitiateMultipart(ctx, relayTestID, 1, MinPartSize); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("InitiateMultipart = %v", err)
	}

	if _, err := store.PresignStagingPart(ctx, relayTestID, "", 1, 1, time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PresignStagingPart = %v", err)
	}

	if err := store.CompleteMultipart(ctx, relayTestID, "", 1, MinPartSize); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("CompleteMultipart = %v", err)
	}
}

// A failed read or delete is recorded as an error sample, and a ranged read
// of a missing blob surfaces the not-found sentinel.
func TestMetricsBlobStoreRecordsFailures(t *testing.T) {
	t.Parallel()

	inner, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	store, reg := newMetricsStore(inner)
	ctx := context.Background()
	absent := sha256Of([]byte("absent"))

	if _, err := store.GetFrom(ctx, absent, 4); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("GetFrom = %v, want ErrBlobNotFound", err)
	}

	if _, err := store.GetSize(ctx, absent); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("GetSize = %v, want ErrBlobNotFound", err)
	}

	if _, err := store.GetMetadata(ctx, "bad"); !errors.Is(err, ErrInvalidBlobID) {
		t.Fatalf("GetMetadata = %v, want ErrInvalidBlobID", err)
	}

	if err := store.Delete(ctx, "bad"); !errors.Is(err, ErrInvalidBlobID) {
		t.Fatalf("Delete = %v, want ErrInvalidBlobID", err)
	}

	for _, op := range []string{"get", "size", "metadata", "delete"} {
		if got := histogramCount(t, reg, op, "error"); got != 1 {
			t.Errorf("%s error samples = %d, want 1", op, got)
		}
	}
}
