// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// uploadPart sends data as part number of the staging object id through its
// presigned part URL.
func uploadPart(t *testing.T, f *fakeAzure, s *AzureBlobStore, id string, number int, data []byte) {
	t.Helper()

	part, err := s.PresignStagingPart(t.Context(), id, "", number, int64(len(data)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if r := f.upload(t, part, data); r.status != http.StatusCreated {
		t.Fatalf("part %d: upload = %d %s", number, r.status, r.body)
	}
}

// requireIncomplete fails unless err is ErrMultipartIncomplete mentioning fragment.
func requireIncomplete(t *testing.T, err error, fragment string) {
	t.Helper()

	if !errors.Is(err, ErrMultipartIncomplete) || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("err = %v, want ErrMultipartIncomplete mentioning %q", err, fragment)
	}
}

func TestAzureMultipartPresignedPartURLs(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	id := uuid.NewString()

	if !s.MultipartSupported() {
		t.Fatal("MultipartSupported = false")
	}

	handle, err := s.InitiateMultipart(ctx, id, 10, 4)
	if err != nil {
		t.Fatal(err)
	}

	for n := 1; n <= 3; n++ {
		part, err := s.PresignStagingPart(ctx, id, handle, n, 4, 0)
		if err != nil {
			t.Fatal(err)
		}

		u := parseURL(t, part.URL)
		q := u.Query()

		if u.Path != "/cont/staging/"+id || q.Get("comp") != "block" || q.Get("blockid") != azureBlockID(n) || q.Get("sp") != "w" {
			t.Errorf("part %d URL = %s", n, part.URL)
		}

		if part.Headers == nil || len(part.Headers) != 0 {
			t.Errorf("part %d headers = %#v, want an empty map", n, part.Headers)
		}
	}
}

func TestAzureMultipartCompletionChecksEveryPart(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	id := uuid.NewString()

	// Parts 1 and 3 arrive; part 2 is missing.
	uploadPart(t, f, s, id, 1, []byte("0123"))
	uploadPart(t, f, s, id, 3, []byte("89"))
	requireIncomplete(t, s.CompleteMultipart(ctx, id, "", 10, 4), "missing parts 2")

	if _, err := s.StatStaging(ctx, id); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("an incomplete upload became visible: %v", err)
	}

	// Part 2 arrives with the wrong size.
	uploadPart(t, f, s, id, 2, []byte("456"))
	requireIncomplete(t, s.CompleteMultipart(ctx, id, "", 10, 4), "wrong size 2")

	// The client re-sends part 2, which replaces the wrong block.
	uploadPart(t, f, s, id, 2, []byte("4567"))

	if err := s.CompleteMultipart(ctx, id, "", 10, 4); err != nil {
		t.Fatalf("CompleteMultipart: %v", err)
	}
}

func TestAzureMultipartAssemblesPromotesAndLocks(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	const size, partSize = 10, 4

	parts := [][]byte{[]byte("0123"), []byte("4567"), []byte("89")}
	content := bytes.Join(parts, nil)
	id := uuid.NewString()

	for n, data := range parts {
		uploadPart(t, f, s, id, n+1, data)
	}

	if err := s.CompleteMultipart(ctx, id, "", size, partSize); err != nil {
		t.Fatalf("CompleteMultipart: %v", err)
	}

	obj, err := s.StatStaging(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if obj.Size != size {
		t.Errorf("assembled size = %d", obj.Size)
	}

	rc, err := s.OpenStaging(ctx, id, obj.Version)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, content) {
		t.Fatalf("assembled = %q, want %q", got, content)
	}

	// Completing again changes nothing.
	if err := s.CompleteMultipart(ctx, id, "", size, partSize); err != nil {
		t.Fatalf("second CompleteMultipart: %v", err)
	}

	if n := f.opCount("put-block-list"); n != 1 {
		t.Errorf("block lists committed = %d, want 1", n)
	}

	// A part URL that is still valid cannot modify the assembled object.
	uploadPart(t, f, s, id, 1, []byte("XXXX"))

	if again, err := s.StatStaging(ctx, id); err != nil || again != obj {
		t.Errorf("after a late part: %+v, %v; want %+v", again, err, obj)
	}

	sha := sha256Hex(content)
	if err := s.PromoteStaging(ctx, id, sha, obj.Version, map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if b := readAll(t, got); !bytes.Equal(b, content) {
		t.Errorf("promoted = %q", b)
	}
}

func TestAzureMultipartPartsAssembleInPartOrder(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	const letters = "abcdefghijkl"

	id := uuid.NewString()

	// Twelve one-byte parts, sent backwards: part 10 sorts before part 2 as text.
	for n := len(letters); n >= 1; n-- {
		uploadPart(t, f, s, id, n, []byte{letters[n-1]})
	}

	if err := s.CompleteMultipart(ctx, id, "", int64(len(letters)), 1); err != nil {
		t.Fatal(err)
	}

	obj, err := s.StatStaging(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := s.OpenStaging(ctx, id, obj.Version)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); string(got) != letters {
		t.Errorf("assembled = %q, want %q", got, letters)
	}
}

func TestAzureMultipartIncompleteNamesTheFirstParts(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})

	err := s.CompleteMultipart(t.Context(), uuid.NewString(), "", 15, 1)
	if !errors.Is(err, ErrMultipartIncomplete) {
		t.Fatalf("err = %v", err)
	}

	if want := "missing parts 1, 2, 3, 4, 5, 6, 7, 8, 9, 10 and 5 more"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err, want)
	}
}

func TestAzureMultipartEmptyObject(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	id := uuid.NewString()

	if err := s.CompleteMultipart(ctx, id, "", 0, MinPartSize); err != nil {
		t.Fatal(err)
	}

	obj, err := s.StatStaging(ctx, id)
	if err != nil || obj.Size != 0 {
		t.Errorf("StatStaging = %+v, %v; want an empty object", obj, err)
	}
}

func TestAzureMultipartPartNumbers(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})

	for _, tc := range []struct {
		number  int
		allowed bool
	}{
		{0, false}, {-1, false}, {1, true}, {MaxParts, true}, {MaxParts + 1, false},
	} {
		_, err := s.PresignStagingPart(t.Context(), uuid.NewString(), "", tc.number, 1, time.Minute)
		if tc.allowed && err != nil {
			t.Errorf("part %d: %v", tc.number, err)
		}

		if !tc.allowed && !errors.Is(err, ErrInvalidPartPlan) {
			t.Errorf("part %d: err = %v, want ErrInvalidPartPlan", tc.number, err)
		}
	}
}

func TestAzureBlockIDsHaveOneLength(t *testing.T) {
	t.Parallel()

	want := len(azureBlockID(1))

	for _, n := range []int{1, 9, 10, 99, 100, MaxParts} {
		if got := len(azureBlockID(n)); got != want {
			t.Errorf("len(azureBlockID(%d)) = %d, want %d (Azure rejects blocks of mixed id length)", n, got, want)
		}
	}
}

func TestAzureDeleteStagingWithOnlyUncommittedBlocks(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	id := uuid.NewString()

	uploadPart(t, f, s, id, 1, []byte("orphan"))

	// Azure has no blob to delete yet; the blocks expire on their own.
	if err := s.DeleteStaging(t.Context(), id); err != nil {
		t.Errorf("DeleteStaging = %v", err)
	}

	if n := f.stagedBlocks("staging/" + id); n != 1 {
		t.Errorf("staged blocks = %d, want the fake to keep them like Azure does", n)
	}
}

func TestAzureDeleteStagingAfterMultipart(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	id := uuid.NewString()

	for n := 1; n <= 2; n++ {
		uploadPart(t, f, s, id, n, []byte(strconv.Itoa(n)))
	}

	if err := s.CompleteMultipart(ctx, id, "", 2, 1); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteStaging(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := s.StatStaging(ctx, id); !errors.Is(err, ErrStagingNotFound) {
		t.Errorf("StatStaging after delete = %v", err)
	}
}
