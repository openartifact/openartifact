// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"io"
	"slices"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
)

func TestAzureChunks(t *testing.T) {
	_, s := newAzureFixture(t, fakeAzureOptions{})

	chunkContract(t, s)
}

// A body of more than one block is staged block by block and committed once;
// the blocks of a body that fails are never committed.
func TestAzureChunksStreamBlocks(t *testing.T) {
	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	tests := []struct {
		name       string
		size       int
		wantBlocks int
	}{
		{"smaller than a block", azureChunkBlockSize - 1, 0},
		{"exactly one block", azureChunkBlockSize, 1},
		{"one block and a byte", azureChunkBlockSize + 1, 2},
		{"two full blocks", 2 * azureChunkBlockSize, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.NewString()
			data := bytes.Repeat([]byte{0xAB}, tc.size)

			before := f.opCount("put-block")

			if n, err := s.PutChunk(ctx, id, bytes.NewReader(data)); err != nil || n != int64(tc.size) {
				t.Fatalf("PutChunk = %d, %v", n, err)
			}

			if got := f.opCount("put-block") - before; got != tc.wantBlocks {
				t.Fatalf("put-block calls = %d, want %d", got, tc.wantBlocks)
			}

			rc, err := s.OpenChunk(ctx, id)
			if err != nil {
				t.Fatal(err)
			}

			if got := readAll(t, rc); !bytes.Equal(got, data) {
				t.Fatalf("OpenChunk returned %d bytes, want %d", len(got), len(data))
			}
		})
	}

	t.Run("failure after staged blocks", func(t *testing.T) {
		id := uuid.NewString()
		body := io.MultiReader(bytes.NewReader(make([]byte, azureChunkBlockSize+10)), iotest.ErrReader(errChunkBody))

		if _, err := s.PutChunk(ctx, id, body); err == nil {
			t.Fatal("PutChunk succeeded")
		}

		requireNoChunk(t, s, id)

		if ids, _ := walkAll(t, s, time.Now().Add(time.Hour)); slices.Contains(ids, id) {
			t.Fatal("the failed chunk is listed")
		}
	})
}

// Chunks live outside blobs/, so listing blobs never sees them.
func TestAzureListIgnoresChunks(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	data := []byte("a real blob")
	sha := sha256Hex(data)

	if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PutChunk(ctx, uuid.NewString(), bytes.NewReader([]byte("chunk"))); err != nil {
		t.Fatal(err)
	}

	ids, _, err := s.List(ctx, "", 100)
	if err != nil || !slices.Equal(ids, []string{sha}) {
		t.Fatalf("List = %v, %v", ids, err)
	}

	if _, found, err := s.ExistsBySHA256(ctx, sha); err != nil || !found {
		t.Fatalf("ExistsBySHA256 = %v, %v", found, err)
	}
}

// The walk reports chunks by age and skips blobs that are not chunk ids.
func TestAzureWalkChunksByAge(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	start := time.Now().Truncate(time.Second)

	f.freeze(start)

	oldID, newID := uuid.NewString(), uuid.NewString()

	if _, err := s.PutChunk(ctx, oldID, bytes.NewReader([]byte("old"))); err != nil {
		t.Fatal(err)
	}

	f.seed("uploads/not-a-uuid", []byte("x"), nil)
	f.advance(2 * time.Hour)

	if _, err := s.PutChunk(ctx, newID, bytes.NewReader([]byte("new"))); err != nil {
		t.Fatal(err)
	}

	ids, _ := walkAll(t, s, start.Add(time.Hour))
	if !slices.Equal(ids, []string{oldID}) {
		t.Fatalf("walk = %v, want only %s", ids, oldID)
	}
}
