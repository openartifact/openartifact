// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func azureTestStore(t *testing.T, baseURL string) *AzureBlobStore {
	t.Helper()

	s, err := NewAzureBlobStore(StorageConfig{
		AzureAccountName:   "acct",
		AzureAccountKey:    base64.StdEncoding.EncodeToString([]byte("key")),
		AzureContainerName: "cont",
	})
	if err != nil {
		t.Fatal(err)
	}

	s.baseURL = baseURL

	return s
}

// STOR-1: Client.Timeout covers reading the body, which cut blobs that take
// longer than the limit to stream. The REST backends must have none.
func TestRESTBackendsHaveNoWholeRequestTimeout(t *testing.T) {
	az := azureTestStore(t, "http://unused")
	if az.client.Timeout != 0 {
		t.Errorf("azure client Timeout = %v, want 0 (it would cut off long streams)", az.client.Timeout)
	}

	oci, err := NewOCIBlobStore(StorageConfig{Endpoint: "https://registry.example.com", Bucket: "oa/blobs"})
	if err != nil {
		t.Fatal(err)
	}

	if oci.client.Timeout != 0 {
		t.Errorf("oci client Timeout = %v, want 0", oci.client.Timeout)
	}
}

// BKP-2: Azure List was a stub that returned nothing, so a full backup had no
// blobs.
func TestAzureListPagesThroughContainer(t *testing.T) {
	const (
		a = "aa11111111111111111111111111111111111111111111111111111111111111"
		b = "bb22222222222222222222222222222222222222222222222222222222222222"
	)

	var markers []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("comp") != "list" || q.Get("prefix") != "blobs/" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}

		markers = append(markers, q.Get("marker"))

		switch q.Get("marker") {
		case "":
			_, _ = w.Write([]byte(`<?xml version="1.0"?><EnumerationResults><Blobs>` +
				`<Blob><Name>blobs/aa/11/` + a + `</Name></Blob><Blob><Name>blobs/zz/junk</Name></Blob>` +
				`</Blobs><NextMarker>m2</NextMarker></EnumerationResults>`))
		default:
			_, _ = w.Write([]byte(`<?xml version="1.0"?><EnumerationResults><Blobs>` +
				`<Blob><Name>blobs/bb/22/` + b + `</Name></Blob></Blobs><NextMarker/></EnumerationResults>`))
		}
	}))
	defer srv.Close()

	s := azureTestStore(t, srv.URL)

	ids, next, err := s.List(context.Background(), "", 10)
	if err != nil || next != "m2" || len(ids) != 1 || ids[0] != a {
		t.Fatalf("page 1 = %v next=%q err=%v, want [%s] m2", ids, next, err, a)
	}

	ids, next, err = s.List(context.Background(), next, 10)
	if err != nil || next != "" || len(ids) != 1 || ids[0] != b {
		t.Fatalf("page 2 = %v next=%q err=%v, want [%s] and end", ids, next, err, b)
	}

	if len(markers) != 2 || markers[1] != "m2" {
		t.Errorf("markers sent = %v, want [\"\" m2]", markers)
	}
}

// BKP-2: the OCI backend cannot enumerate blobs; it must say so instead of
// pretending the store is empty.
func TestOCIListIsExplicitlyUnsupported(t *testing.T) {
	oci, err := NewOCIBlobStore(StorageConfig{Endpoint: "https://registry.example.com", Bucket: "oa/blobs"})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := oci.List(context.Background(), "", 10); !errors.Is(err, ErrListNotSupported) {
		t.Fatalf("OCI List error = %v, want ErrListNotSupported", err)
	}
}

// BKP-8: a missing blob must be distinguishable from a transport error.
func TestFilesystemGetMissingWrapsErrBlobNotFound(t *testing.T) {
	fs, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = fs.Get(context.Background(), strings.Repeat("c", 64))
	if !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrBlobNotFound", err)
	}
}

// STOR-2: every x-ms-* header sent must be signed, metadata names must be
// valid Azure identifiers, and an empty blob must go out with Content-Length 0
// rather than chunked.
func TestAzurePutSignsMetadataAndEmptyBlob(t *testing.T) {
	var gotAuth, gotCL, gotMeta string

	var chunked bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		gotAuth = r.Header.Get("Authorization")
		gotCL = r.Header.Get("Content-Length")
		gotMeta = r.Header.Get("X-Ms-Meta-Content_type")
		chunked = len(r.TransferEncoding) > 0

		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	s := azureTestStore(t, srv.URL)
	sum := sha256.Sum256(nil)

	if _, err := s.Put(context.Background(), bytes.NewReader(nil),
		map[string]string{"sha256": hex.EncodeToString(sum[:]), "content-type": "text/x"}); err != nil {
		t.Fatal(err)
	}

	if chunked || gotCL != "0" {
		t.Errorf("empty blob: Content-Length=%q chunked=%v, want 0/false", gotCL, chunked)
	}

	if gotMeta != "text/x" {
		t.Errorf("metadata header = %q, want sanitized x-ms-meta-content_type", gotMeta)
	}

	if gotAuth == "" {
		t.Error("missing Authorization header")
	}

	if got := canonicalizedMSHeaders(http.Header{"X-Ms-B": {"2"}, "X-Ms-A": {" 1 "}, "Content-Type": {"x"}}); got != "x-ms-a:1\nx-ms-b:2\n" {
		t.Errorf("canonicalizedMSHeaders = %q", got)
	}
}
