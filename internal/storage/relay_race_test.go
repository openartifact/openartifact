// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// signalReader reports its first Read, which happens once putPart holds the
// upload's lock, then serves from the wrapped reader.
type signalReader struct {
	io.Reader

	once    sync.Once
	started chan struct{}
}

func (s *signalReader) Read(p []byte) (int, error) {
	s.once.Do(func() { close(s.started) })

	return s.Reader.Read(p)
}

type raceFixture struct {
	env      *relayEnv
	relay    *relay
	data     []byte
	size     int64
	partSize int64
}

const raceParts = 3

// newRaceFixture initiates a three-part upload and writes every part directly
// (no HTTP, so nothing auto-finalizes): all markers exist, the object does not.
func newRaceFixture(t *testing.T) *raceFixture {
	t.Helper()

	env, fsys := newFSRelayEnv(t)
	partSize := MinPartSize
	size := raceParts * partSize
	data := bytes.Repeat([]byte("0123456789abcdef"), int(size)/16)

	if _, err := env.multi.InitiateMultipart(context.Background(), relayTestID, size, partSize); err != nil {
		t.Fatal(err)
	}

	f := &raceFixture{env: env, relay: fsys.relay, data: data, size: size, partSize: partSize}
	meta := relayMeta{Size: size, PartSize: partSize, PartCount: raceParts}

	for n := 1; n <= raceParts; n++ {
		if _, status := f.relay.putPart(relayTestID, n, meta, partSize, bytes.NewReader(f.part(n))); status != http.StatusOK {
			t.Fatalf("part %d status = %d", n, status)
		}
	}

	return f
}

func (f *raceFixture) part(n int) []byte {
	start := int64(n-1) * f.partSize

	return f.data[start : start+f.partSize]
}

// slowRewrite starts a retry of part 1 whose body the caller feeds through the
// returned writer. It returns once the PUT holds the upload's lock.
func (f *raceFixture) slowRewrite(t *testing.T) (*io.PipeWriter, <-chan int) {
	t.Helper()

	pr, pw := io.Pipe()
	body := &signalReader{Reader: pr, started: make(chan struct{})}
	finished := make(chan int, 1)
	meta := relayMeta{Size: f.size, PartSize: f.partSize, PartCount: raceParts}

	go func() {
		_, status := f.relay.putPart(relayTestID, 1, meta, f.partSize, body)
		finished <- status
	}()

	// putPart reads the body only after taking the shared lock.
	if _, err := pw.Write(f.part(1)[:1]); err != nil {
		t.Fatal(err)
	}

	<-body.started

	return pw, finished
}

// finishRewrite sends the rest of part 1's bytes and waits for the PUT.
func (f *raceFixture) finishRewrite(t *testing.T, pw *io.PipeWriter, done <-chan int) int {
	t.Helper()

	if _, err := pw.Write(f.part(1)[1:]); err != nil {
		t.Fatal(err)
	}

	_ = pw.Close()

	return <-done
}

// A part PUT that is still writing when complete starts is waited for; any PUT
// that starts after complete began is refused, and the published object is the
// bytes the in-flight PUT finished writing.
func TestRelayCompleteWaitsForInFlightPutAndRefusesNewOnes(t *testing.T) {
	t.Parallel()

	f := newRaceFixture(t)
	ctx := context.Background()
	sealed := make(chan struct{})
	f.relay.afterSeal = func() { close(sealed) }

	pw, done := f.slowRewrite(t)

	completed := make(chan error, 1)

	go func() {
		completed <- f.env.multi.CompleteMultipart(ctx, relayTestID, "", f.size, f.partSize)
	}()

	<-sealed

	urlPart2, err := f.env.multi.PresignStagingPart(ctx, relayTestID, "", 2, f.partSize, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	mustStatus(t, relayPut(t, urlPart2.URL, f.part(2)), http.StatusConflict)

	// The in-flight writer still holds the lock, so nothing is published yet.
	if _, err := f.env.store.StatStaging(ctx, relayTestID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("object published while a part was being written: %v", err)
	}

	if status := f.finishRewrite(t, pw, done); status != http.StatusOK {
		t.Fatalf("in-flight PUT status = %d", status)
	}

	if err := <-completed; err != nil {
		t.Fatalf("complete: %v", err)
	}

	obj, err := f.env.store.StatStaging(ctx, relayTestID)
	if err != nil {
		t.Fatal(err)
	}

	assertStagedBytes(t, f, obj)

	// Nothing can reach the published inode any more.
	meta := relayMeta{Size: f.size, PartSize: f.partSize, PartCount: raceParts}
	if _, status := f.relay.putPart(relayTestID, 1, meta, f.partSize, bytes.NewReader(bytes.Repeat([]byte{'X'}, int(f.partSize)))); status != http.StatusConflict {
		t.Fatalf("PUT after complete status = %d, want 409", status)
	}

	assertStagedBytes(t, f, obj)
}

func assertStagedBytes(t *testing.T, f *raceFixture, obj StagedObject) {
	t.Helper()

	rc, err := f.env.store.OpenStaging(context.Background(), relayTestID, obj.Version)
	if err != nil {
		t.Fatalf("open staged object: %v", err)
	}

	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, f.data) {
		t.Fatal("published object differs from the uploaded bytes")
	}
}

// When complete cannot get past an in-flight PUT (its context ends) the
// upload is reopened: nothing is published and parts can be uploaded again.
func TestRelayCompleteGivingUpReopensTheUpload(t *testing.T) {
	t.Parallel()

	f := newRaceFixture(t)
	pw, done := f.slowRewrite(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := f.env.multi.CompleteMultipart(ctx, relayTestID, "", f.size, f.partSize)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("complete err = %v, want context canceled", err)
	}

	if _, err := f.env.store.StatStaging(context.Background(), relayTestID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("object published by a failed complete: %v", err)
	}

	if _, err := os.Stat(f.relay.sealPath(relayTestID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("seal left behind: %v", err)
	}

	if status := f.finishRewrite(t, pw, done); status != http.StatusOK {
		t.Fatalf("in-flight PUT status = %d", status)
	}

	if err := f.env.multi.CompleteMultipart(context.Background(), relayTestID, "", f.size, f.partSize); err != nil {
		t.Fatalf("complete after retry: %v", err)
	}

	obj, err := f.env.store.StatStaging(context.Background(), relayTestID)
	if err != nil {
		t.Fatal(err)
	}

	assertStagedBytes(t, f, obj)
}
