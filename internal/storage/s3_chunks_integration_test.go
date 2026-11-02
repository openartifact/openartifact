// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package storage

import (
	"bytes"
	"crypto/rand"
	"io"
	"slices"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
)

// TestS3Chunks runs the chunk contract against the bucket named by TEST_S3_*.
func TestS3Chunks(t *testing.T) {
	chunkContract(t, integrationS3(t))
}

// A body longer than one part goes through the multipart path without a spool
// and reads back identical; a failing long body leaves no object.
func TestS3ChunksMultipart(t *testing.T) {
	s := integrationS3(t)
	ctx := t.Context()

	data := make([]byte, s3PartSize+1234)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	id := uuid.NewString()
	t.Cleanup(func() { _ = s.DeleteChunk(ctx, id) })

	n, err := s.PutChunk(ctx, id, iotest.OneByteReader(bytes.NewReader(data[:100])))
	if err != nil || n != 100 {
		t.Fatalf("small PutChunk = %d, %v", n, err)
	}

	if n, err := s.PutChunk(ctx, id, io.MultiReader(bytes.NewReader(data))); err != nil || n != int64(len(data)) {
		t.Fatalf("PutChunk = %d, %v", n, err)
	}

	rc, err := s.OpenChunk(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, data) {
		t.Fatal("OpenChunk returned different bytes")
	}

	failID := uuid.NewString()
	if _, err := s.PutChunk(ctx, failID, io.MultiReader(bytes.NewReader(data), iotest.ErrReader(errChunkBody))); err == nil {
		t.Fatal("PutChunk of a failing body succeeded")
	}

	requireNoChunk(t, s, failID)
}

// Chunks are invisible to blob listing and found by the chunk walk.
func TestS3ChunksStayOutOfBlobListing(t *testing.T) {
	s := integrationS3(t)
	ctx := t.Context()
	id := uuid.NewString()

	if _, err := s.PutChunk(ctx, id, bytes.NewReader([]byte("chunk"))); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.DeleteChunk(ctx, id) })

	token := ""

	for {
		ids, next, err := s.List(ctx, token, 1000)
		if err != nil {
			t.Fatal(err)
		}

		if slices.Contains(ids, id) {
			t.Fatal("List returned a chunk")
		}

		if next == "" {
			break
		}

		token = next
	}

	if ids, _ := walkAll(t, s, time.Now().Add(time.Hour)); !slices.Contains(ids, id) {
		t.Fatal("WalkChunks missed the chunk")
	}
}
