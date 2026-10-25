// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"os"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

const (
	// maxResumeRetries bounds the consecutive attempts at one blob that move
	// no bytes before the pull gives up (the partial file is kept for the next
	// pull).
	maxResumeRetries = 4
	// maxTransferAttempts bounds all the attempts at one blob within a pull,
	// however much progress each makes.
	maxTransferAttempts = 32
	// defaultResumeBackoff is the pause before the first retry; it doubles up
	// to maxResumeBackoff.
	defaultResumeBackoff = 500 * time.Millisecond
	maxResumeBackoff     = 8 * time.Second
)

// download copies a blob of the peer into the spool and returns the path of
// the verified file, which the caller owns. The bytes arrive in <sha256>.part:
// a dropped connection is resumed with a Range request from the length of that
// file (the prefix is hashed again), and a partial left by an earlier pull is
// continued the same way. A peer that ignores the range sends the whole blob
// and the partial is restarted. The sha256 of the whole blob is verified after
// receipt; on a mismatch the partial is discarded and the unit deferred. The
// returned path is empty when another pull stored the blob in the meantime.
func (e *Engine) download(ctx context.Context, pc *pullCtx, key string, ref BlobRef) (string, error) {
	release, err := e.spool.acquire(ctx, ref.SHA256)
	if err != nil {
		return "", err
	}
	defer release()

	// A concurrent pull of another repository may have stored it while this
	// one waited for the partial.
	if have, err := e.Blobs.Exists(ctx, ref.SHA256); err == nil && have {
		return "", nil
	}

	f, err := e.spool.openPartial(ref.SHA256)
	if err != nil {
		return "", err
	}

	sum, err := e.transfer(ctx, pc, key, ref, f)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close partial blob: %w", cerr)
	}

	if err == nil && sum != ref.SHA256 {
		err = deferred("digest_mismatch", fmt.Errorf("%w: %s hashes to %s", errBlobDigestMismatch, ref.SHA256, sum))
	}

	if err != nil {
		e.afterFailedDownload(err, ref.SHA256)

		return "", err
	}

	return e.spool.promote(ref.SHA256)
}

// afterFailedDownload keeps the partial file of an interrupted transfer for the
// next pull, and drops the bytes of a blob that failed verification. The spool
// caps are re-applied either way.
func (e *Engine) afterFailedDownload(err error, sha string) {
	var de *deferError

	if errors.As(err, &de) && de.reason == "digest_mismatch" {
		e.spool.discard(sha)
	}

	e.spool.discardEmpty(sha)
	e.spool.sweep(time.Now())
}

// transfer fetches the blob into f, resuming after interruptions, and returns
// the sha256 of the complete content.
func (e *Engine) transfer(ctx context.Context, pc *pullCtx, key string, ref BlobRef, f *os.File) (string, error) {
	var failures, attempts int

	for {
		attempts++

		before, err := fileSize(f)
		if err != nil {
			return "", err
		}

		sum, err := e.fetchRange(ctx, pc, key, ref, f)
		if err == nil {
			return sum, nil
		}

		if !retryableTransfer(ctx, err) {
			return "", err
		}

		_ = f.Sync() // keep what arrived across a crash too

		after, serr := fileSize(f)
		if serr != nil {
			return "", serr
		}

		if after > before {
			failures = 0
		} else {
			failures++
		}

		if failures > maxResumeRetries || attempts >= maxTransferAttempts {
			return "", err
		}

		pc.logResume(e, ref, after, err)

		if err := sleepCtx(ctx, e.resumePause(failures)); err != nil {
			return "", err
		}
	}
}

func (pc *pullCtx) logResume(e *Engine, ref BlobRef, have int64, err error) {
	e.Log.Warnw("federation: blob transfer interrupted, resuming",
		"repo", pc.repo.Name, "peer", pc.peer.Name, "sha256", ref.SHA256, "received", have, "size", ref.Size, "error", err)
}

// resumePause is the wait before retry number failures (1 = first retry).
func (e *Engine) resumePause(failures int) time.Duration {
	d := e.resumeBackoff
	if d <= 0 {
		d = defaultResumeBackoff
	}

	for range max(failures-1, 0) {
		d = min(d*2, maxResumeBackoff)
	}

	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("blob transfer: %w", ctx.Err())
	}
}

// fetchRange makes one request for the rest of the blob and appends the body
// to f. It returns the sha256 of f once it holds the whole blob; any other
// outcome is an error (the bytes received so far stay in f).
func (e *Engine) fetchRange(ctx context.Context, pc *pullCtx, key string, ref BlobRef, f *os.File) (string, error) {
	have, err := fileSize(f)
	if err != nil {
		return "", err
	}

	// A partial longer than the blob cannot be a prefix of it.
	if ref.Size > 0 && have > ref.Size {
		have = 0

		if err = restart(f); err != nil {
			return "", err
		}
	}

	h := sha256.New()

	// Everything was downloaded earlier and only verification is left.
	if ref.Size > 0 && have == ref.Size {
		if err := hashPrefix(h, f, have); err != nil {
			return "", err
		}

		return hex.EncodeToString(h.Sum(nil)), nil
	}

	stream, err := pc.client.Blob(ctx, key, ref.SHA256, have)
	if errors.Is(err, errRangeUnsatisfiable) && have > 0 {
		// The peer's copy is not what the partial was cut from.
		have = 0

		if err = restart(f); err != nil {
			return "", err
		}

		stream, err = pc.client.Blob(ctx, key, ref.SHA256, 0)
	}

	if errors.Is(err, ErrNotFound) {
		return "", deferred("blob_missing", fmt.Errorf("blob %s is not available at the peer: %w", ref.SHA256, err))
	}

	if err != nil {
		return "", err
	}
	defer stream.Body.Close()

	if stream.Offset != have {
		// The peer ignored the range and sends the blob from its start.
		have = 0

		if err = restart(f); err != nil {
			return "", err
		}
	} else if have > 0 && e.Metrics != nil {
		e.Metrics.Resumed.WithLabelValues(pc.repo.Name, pc.peer.Name).Inc()
	}

	if err := hashPrefix(h, f, have); err != nil {
		return "", err
	}

	if _, err := f.Seek(have, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek partial blob: %w", err)
	}

	if err := appendBody(f, h, stream.Body, ref, have); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// appendBody copies the rest of the blob into f and h. A body that fails
// midway is an interruption (see streamError); one that ends cleanly short of
// the declared size, or runs past it, is a bad blob.
func appendBody(f io.Writer, h hash.Hash, body io.Reader, ref BlobRef, have int64) error {
	var src io.Reader = &streamReader{r: body}

	remaining := int64(-1)

	if ref.Size > 0 {
		remaining = ref.Size - have
		src = io.LimitReader(src, remaining+1)
	}

	n, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		return fmt.Errorf("download blob %s: %w", ref.SHA256, err)
	}

	switch {
	case remaining >= 0 && n > remaining:
		return deferred("digest_mismatch", fmt.Errorf("%w: %s", errBlobTooLong, ref.SHA256))
	case remaining >= 0 && n < remaining:
		// A connection that drops fails the read above; ending cleanly short
		// is the peer serving fewer bytes than it declared.
		return deferred("digest_mismatch", fmt.Errorf("%w: %s: %d of %d bytes", errBlobTooShort, ref.SHA256, have+n, ref.Size))
	}

	return nil
}

// streamError marks a failure to read the response body: a dropped connection,
// which a resume can recover from, as opposed to a failure writing it locally.
type streamError struct{ err error }

func (e *streamError) Error() string { return e.err.Error() }
func (e *streamError) Unwrap() error { return e.err }

// streamReader reports the read errors of the peer's body as streamError.
type streamReader struct{ r io.Reader }

func (s *streamReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &streamError{err: err}
	}

	return n, err //nolint:wrapcheck // io.Reader contract: EOF must pass through unwrapped
}

// retryableTransfer reports whether a failed attempt at a blob is worth
// resuming: the connection dropped or timed out while the pull itself is still
// wanted. Answers of the peer (HTTP errors, a missing or quarantined blob) and
// local failures are not retried here.
func retryableTransfer(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, netutil.ErrBlockedAddress) {
		return false
	}

	if _, ok := errors.AsType[*streamError](err); ok {
		return true
	}

	if _, ok := errors.AsType[*HTTPError](err); ok {
		return false
	}

	if _, ok := errors.AsType[*deferError](err); ok {
		return false
	}

	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}

	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func fileSize(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat partial blob: %w", err)
	}

	return st.Size(), nil
}

// restart empties the partial file.
func restart(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("reset partial blob: %w", err)
	}

	return nil
}

// hashPrefix feeds the first n bytes of f to h.
func hashPrefix(h hash.Hash, f *os.File, n int64) error {
	if n == 0 {
		return nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek partial blob: %w", err)
	}

	got, err := io.Copy(h, io.LimitReader(f, n))
	if err != nil {
		return fmt.Errorf("hash partial blob: %w", err)
	}

	if got != n {
		return fmt.Errorf("hash partial blob: %w", errPartialShort)
	}

	return nil
}
