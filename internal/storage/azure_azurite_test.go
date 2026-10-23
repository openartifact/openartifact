// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	azuriteAccount = "devstoreaccount1"
	// azuriteKey is the well-known development key of Azurite and of the old
	// Azure Storage Emulator, published in Microsoft's documentation.
	azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
)

// TestAzuriteSmoke runs the store against a real Azurite (the emulator is
// path-style, which the in-process fake cannot vouch for) when AZURITE_ENDPOINT
// is set, e.g. after
//
//	podman run -d --rm -p 10000:10000 mcr.microsoft.com/azure-storage/azurite \
//	  azurite-blob --blobHost 0.0.0.0 --skipApiVersionCheck
//
// with AZURITE_ENDPOINT=http://127.0.0.1:10000/devstoreaccount1. It always
// skips otherwise: it needs a container runtime, not just a CI flag.
func TestAzuriteSmoke(t *testing.T) {
	endpoint := os.Getenv("AZURITE_ENDPOINT")
	if endpoint == "" {
		t.Skip("AZURITE_ENDPOINT is not set")
	}

	s, err := NewAzureBlobStore(StorageConfig{
		AzureAccountName:   azuriteAccount,
		AzureAccountKey:    azuriteKey,
		AzureContainerName: "smoke-" + strings.ToLower(uuid.NewString()[:8]),
		AzureEndpoint:      endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}

	createAzuriteContainer(t, s)

	t.Run("shared key and download SAS", func(t *testing.T) { azuriteBlobs(t, s) })
	t.Run("single request staging", func(t *testing.T) { azuriteStaging(t, s) })
	t.Run("promotion", func(t *testing.T) { azuritePromote(t, s) })
	t.Run("multipart staging", func(t *testing.T) { azuriteMultipart(t, s) })
}

func createAzuriteContainer(t *testing.T, s *AzureBlobStore) {
	t.Helper()

	call := func(ctx context.Context, method string) int {
		resp, err := s.do(ctx, azureCall{method: method, query: url.Values{"restype": {"container"}}})
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		return resp.StatusCode
	}

	if status := call(t.Context(), http.MethodPut); status != http.StatusCreated {
		t.Fatalf("create container = %d", status)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		call(ctx, http.MethodDelete)
	})
}

func azuriteBlobs(t *testing.T, s *AzureBlobStore) {
	t.Helper()

	ctx := t.Context()
	content := []byte("azurite blob")
	sha := sha256Hex(content)

	if err := s.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}

	if _, err := s.Put(ctx, bytes.NewReader(content), map[string]string{"sha256": sha, "format": "raw"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if size, err := s.GetSize(ctx, sha); err != nil || size != int64(len(content)) {
		t.Fatalf("GetSize = %d, %v", size, err)
	}

	if meta, err := s.GetMetadata(ctx, sha); err != nil || meta["Format"] != "raw" {
		t.Fatalf("GetMetadata = %v, %v", meta, err)
	}

	if ids, _, err := s.List(ctx, "", 10); err != nil || len(ids) != 1 || ids[0] != sha {
		t.Fatalf("List = %v, %v", ids, err)
	}

	raw, err := s.PresignGet(ctx, sha, time.Minute, PresignGetOptions{ContentDisposition: `attachment; filename="a.txt"`, ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}

	r := sendHTTP(t, http.DefaultClient, http.MethodGet, raw, nil, nil)
	if r.status != http.StatusOK || !bytes.Equal(r.body, content) {
		t.Fatalf("SAS download = %d %q", r.status, r.body)
	}

	if r.header.Get("Content-Disposition") != `attachment; filename="a.txt"` || r.header.Get("Content-Type") != "text/plain" {
		t.Errorf("response overrides = %q / %q", r.header.Get("Content-Disposition"), r.header.Get("Content-Type"))
	}

	altered := withQuery(t, raw, func(q url.Values) { q.Set("rsct", "text/html") })
	if r := sendHTTP(t, http.DefaultClient, http.MethodGet, altered, nil, nil); r.status != http.StatusForbidden {
		t.Errorf("altered SAS = %d, want 403", r.status)
	}
}

func azuriteUpload(t *testing.T, s *AzureBlobStore, content []byte) (string, string) {
	t.Helper()

	id := uuid.NewString()

	put, err := s.PresignStagingPut(t.Context(), id, int64(len(content)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if r := sendHTTP(t, http.DefaultClient, http.MethodPut, put.URL, put.Headers, content); r.status != http.StatusCreated {
		t.Fatalf("staging upload = %d %s", r.status, r.body)
	}

	obj, err := s.StatStaging(t.Context(), id)
	if err != nil || obj.Size != int64(len(content)) {
		t.Fatalf("StatStaging = %+v, %v", obj, err)
	}

	return id, obj.Version
}

func azuriteStaging(t *testing.T, s *AzureBlobStore) {
	t.Helper()

	ctx := t.Context()
	content := []byte("azurite staged object")
	sha := sha256Hex(content)
	id, version := azuriteUpload(t, s, content)

	rc, err := s.OpenStaging(ctx, id, version)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, content) {
		t.Fatalf("OpenStaging = %q", got)
	}

	// The object is swapped through the still-valid URL: the verified version
	// must not be promotable any more.
	put, err := s.PresignStagingPut(ctx, id, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if r := sendHTTP(t, http.DefaultClient, http.MethodPut, put.URL, put.Headers, []byte("swapped")); r.status != http.StatusCreated {
		t.Fatalf("overwrite = %d", r.status)
	}

	if err := s.PromoteStaging(ctx, id, sha, version, nil); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("PromoteStaging(old version) = %v, want ErrStagingChanged", err)
	}

	if ok, err := s.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("Exists after a refused promotion = %v, %v", ok, err)
	}
}

func azuritePromote(t *testing.T, s *AzureBlobStore) {
	t.Helper()

	ctx := t.Context()
	content := []byte("azurite promoted object")
	sha := sha256Hex(content)
	id, version := azuriteUpload(t, s, content)

	metadata := map[string]string{"sha256": sha, "format": "raw"}
	for range 2 {
		if err := s.PromoteStaging(ctx, id, sha, version, metadata); err != nil {
			t.Fatalf("PromoteStaging: %v", err)
		}
	}

	got, err := s.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if b := readAll(t, got); !bytes.Equal(b, content) {
		t.Fatalf("promoted = %q", b)
	}

	if meta, err := s.GetMetadata(ctx, sha); err != nil || meta["Format"] != "raw" || len(meta) != 1 {
		t.Errorf("promoted metadata = %v, %v", meta, err)
	}

	var walked []string

	if err := s.WalkStaging(ctx, time.Now().Add(time.Hour), func(ids []string) error {
		walked = append(walked, ids...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(walked, id) {
		t.Errorf("WalkStaging = %v, want it to contain %s", walked, id)
	}

	if err := s.DeleteStaging(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := s.StatStaging(ctx, id); !errors.Is(err, ErrStagingNotFound) {
		t.Errorf("StatStaging after delete = %v", err)
	}
}

func azuriteMultipart(t *testing.T, s *AzureBlobStore) {
	t.Helper()

	ctx := t.Context()
	id := uuid.NewString()
	parts := [][]byte{[]byte("0123"), []byte("4567"), []byte("89")}
	content := bytes.Join(parts, nil)
	sha := sha256Hex(content)

	put := func(n int, data []byte) {
		t.Helper()

		part, err := s.PresignStagingPart(ctx, id, "", n, int64(len(data)), time.Hour)
		if err != nil {
			t.Fatal(err)
		}

		if r := sendHTTP(t, http.DefaultClient, http.MethodPut, part.URL, part.Headers, data); r.status != http.StatusCreated {
			t.Fatalf("part %d = %d %s", n, r.status, r.body)
		}
	}

	put(1, parts[0])
	put(3, parts[2])

	if err := s.CompleteMultipart(ctx, id, "", int64(len(content)), 4); !errors.Is(err, ErrMultipartIncomplete) {
		t.Fatalf("incomplete: err = %v", err)
	}

	put(2, parts[1])

	for range 2 {
		if err := s.CompleteMultipart(ctx, id, "", int64(len(content)), 4); err != nil {
			t.Fatalf("CompleteMultipart: %v", err)
		}
	}

	obj, err := s.StatStaging(ctx, id)
	if err != nil || obj.Size != int64(len(content)) {
		t.Fatalf("StatStaging = %+v, %v", obj, err)
	}

	put(1, []byte("XXXX")) // a late part must not change the assembled object

	if err := s.PromoteStaging(ctx, id, sha, obj.Version, map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}

	rc, err := s.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, content) {
		t.Errorf("promoted = %q, want %q", got, content)
	}

	if err := s.DeleteStaging(ctx, id); err != nil {
		t.Fatal(err)
	}
}
