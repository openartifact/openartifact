// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"

	"github.com/openartifact/openartifact/internal/telemetry"
)

const (
	// minUploadPart is the smallest part S3 accepts in a multipart upload (the
	// last one excepted). What an append leaves below it is kept as the tail.
	minUploadPart = 5 << 20

	// uploadPartsInFlight is how many parts of one append are sent at once; with
	// the part being filled that is (n+1) * s3PartSize bytes of memory (64 MiB).
	uploadPartsInFlight = 3

	// uploadCleanupTimeout bounds the removal of what a committed append
	// superseded.
	uploadCleanupTimeout = 30 * time.Second
)

var _ UploadStore = (*S3BlobStore)(nil)

// UploadsSupported implements UploadStore: uploads are objects of the bucket
// (also on GCS, Garage and SeaweedFS, which share this store).
func (s *S3BlobStore) UploadsSupported() bool { return true }

// s3UploadState is the opaque State of an upload. One upload is one S3
// multipart upload U whose first P parts hold the first B bytes; the bytes after
// them (the tail, under minUploadPart) are the immutable object tailKey(id, offset),
// because a part below the minimum size cannot sit anywhere but at the end.
type s3UploadState struct {
	U string `json:"u,omitempty"`
	P int    `json:"p,omitempty"`
	B int64  `json:"b,omitempty"`
}

// parseUploadState decodes a State and checks it against the offset it was
// stored with.
func parseUploadState(state string, offset int64) (s3UploadState, error) {
	var st s3UploadState

	if state != "" {
		if err := json.Unmarshal([]byte(state), &st); err != nil {
			return st, fmt.Errorf("%w: unreadable upload state: %w", ErrUploadDataLost, err)
		}
	}

	if st.B < 0 || st.B > offset || offset-st.B >= minUploadPart || st.P < 0 || st.P > MaxParts || (st.P > 0) != (st.U != "") {
		return st, fmt.Errorf("%w: inconsistent upload state %q at offset %d", ErrUploadDataLost, state, offset)
	}

	return st, nil
}

// String encodes the State; a new upload with no part yet has none.
func (st s3UploadState) String() string {
	if st == (s3UploadState{}) {
		return ""
	}

	b, _ := json.Marshal(st) //nolint:errchkjson // two scalars and a string cannot fail to encode

	return string(b)
}

// tailKey is the object holding the bytes of upload id that are not in a part
// yet, named after the offset they end at so that an append that fails can
// never alter the tail the committed position refers to.
func tailKey(key string, offset int64) string {
	return key + "." + strconv.FormatInt(offset, 10)
}

// partBuf is a reusable buffer of one part.
type partBuf struct{ b []byte }

// uploadBufs hands out s3PartSize buffers, so that appends do not allocate a
// part of memory each (and nothing is spooled to disk).
var uploadBufs = newPartPool(s3PartSize) //nolint:gochecknoglobals // a pool of reusable buffers

func newPartPool(size int) *sync.Pool {
	return &sync.Pool{New: func() any { return &partBuf{b: make([]byte, size)} }}
}

// partCutter splits a byte stream into parts and sends them with a few in
// flight. min is the smallest remainder that may become a part of its own (a
// smaller one is the tail).
type partCutter struct {
	// pool holds the buffers of one part each; their length is the part size.
	pool     *sync.Pool
	min      int
	inflight int
	// first is the number of the first part the stream becomes.
	first int
	// put sends part number n; tail stores the remainder below min, which ends
	// at stream offset end (counted from the start of the stream passed to cut).
	put  func(ctx context.Context, n int, data []byte) error
	tail func(ctx context.Context, end int64, data []byte) error
}

// cutResult is what cut stored: total bytes read, split into a number of
// parts holding partBytes bytes and a tail of the rest.
type cutResult struct {
	total, partBytes int64
	parts            int
}

// cut reads r to its end after the head bytes already in buf (b.b[:head],
// buf is taken over) and stores them as parts and a tail. On failure nothing
// is rolled back: parts are overwritten by a retry.
func (c *partCutter) cut(ctx context.Context, buf *partBuf, head int, r io.Reader) (cutResult, error) {
	res := cutResult{total: int64(head)}

	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(c.inflight)

	send := func(buf *partBuf, n int, run func(context.Context, []byte) error) {
		group.Go(func() error {
			defer c.pool.Put(buf)

			return run(gctx, buf.b[:n])
		})
	}

	fill := head

	var readErr error

	for readErr == nil && gctx.Err() == nil {
		n, err := readInto(r, buf.b[fill:])
		fill += n
		res.total += int64(n)

		if err != nil {
			// Anything but the end of the stream is the caller's error.
			if !errors.Is(err, io.EOF) {
				readErr = err
			}

			break
		}

		// The buffer is full, so more may follow: it is one part.
		number := c.first + res.parts
		if number > MaxParts {
			readErr = ErrUploadTooLarge

			break
		}

		res.parts++
		res.partBytes += int64(fill)

		send(buf, fill, func(ctx context.Context, data []byte) error { return c.put(ctx, number, data) })

		buf, fill = c.pool.Get().(*partBuf), 0 //nolint:forcetypeassert // the pool only holds *partBuf
	}

	// The stream ended (a full buffer is sent in the loop), so what is left in
	// buf is the remainder.
	if readErr == nil && gctx.Err() == nil {
		readErr = c.sendRemainder(send, buf, fill, &res)
	} else {
		c.pool.Put(buf)
	}

	waitErr := group.Wait()

	switch {
	case waitErr != nil:
		return res, waitErr
	case readErr != nil:
		return res, readErr
	}

	return res, nil
}

// sendRemainder stores the last fill bytes: a part of its own when large
// enough, else the tail. The first byte of the head guarantees a non-empty
// stream, but the remainder itself may be empty (the stream ended on a part).
func (c *partCutter) sendRemainder(
	send func(*partBuf, int, func(context.Context, []byte) error),
	buf *partBuf,
	fill int,
	res *cutResult,
) error {
	switch {
	case fill == 0:
		c.pool.Put(buf)
	case fill >= c.min:
		number := c.first + res.parts
		if number > MaxParts {
			c.pool.Put(buf)

			return ErrUploadTooLarge
		}

		res.parts++
		res.partBytes += int64(fill)

		send(buf, fill, func(ctx context.Context, data []byte) error { return c.put(ctx, number, data) })
	default:
		end := res.total

		send(buf, fill, func(ctx context.Context, data []byte) error { return c.tail(ctx, end, data) })
	}

	return nil
}

// readInto reads until buf is full or r ends. It returns io.EOF only when r ended
// before buf was full; the error of r otherwise, as it is (io.ReadFull would
// turn an io.ErrUnexpectedEOF of r into a normal end of the stream).
func readInto(r io.Reader, buf []byte) (int, error) {
	n := 0

	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m

		if err != nil {
			return n, err //nolint:wrapcheck // the caller's reader error, reported as is
		}
	}

	return n, nil
}

// AppendUpload implements UploadStore. The tail is read back first and goes
// in front of the body, which is cut into parts of s3PartSize sent while it
// streams; what is left under minUploadPart is stored as the new tail. Nothing
// of the old position is touched until commit returned.
func (s *S3BlobStore) AppendUpload(
	ctx context.Context,
	id string,
	at UploadPosition,
	r io.Reader,
	commit func(ctx context.Context, state string, n int64) error,
) (err error) {
	key, err := uploadKey(id)
	if err != nil {
		return err
	}

	st, err := parseUploadState(at.State, at.Offset)
	if err != nil {
		return err
	}

	ctx, end := telemetry.StartSpan(ctx, "storage.s3.upload_append")
	defer func() { end(err) }()

	// An empty body changes nothing: no tail is fetched, nothing is committed.
	var first [1]byte

	if _, rerr := readInto(r, first[:]); rerr != nil {
		if errors.Is(rerr, io.EOF) {
			return nil
		}

		return fmt.Errorf("s3: read upload body: %w", rerr)
	}

	tailLen := int(at.Offset - st.B)
	buf := uploadBufs.Get().(*partBuf) //nolint:forcetypeassert // the pool only holds *partBuf

	if err := s.loadTail(ctx, key, at.Offset, buf.b[:tailLen]); err != nil {
		uploadBufs.Put(buf)

		return err
	}

	buf.b[tailLen] = first[0]

	var (
		mu        sync.Mutex
		uploadID  = st.U
		createErr error
	)

	cutter := &partCutter{
		pool: uploadBufs, min: minUploadPart, inflight: uploadPartsInFlight, first: st.P + 1,
		put: func(ctx context.Context, n int, data []byte) error {
			// The multipart upload starts with the first part to send.
			mu.Lock()

			if uploadID == "" && createErr == nil {
				uploadID, createErr = s.createUpload(ctx, key)
			}

			current, cerr := uploadID, createErr
			mu.Unlock()

			if cerr != nil {
				return cerr
			}

			return s.uploadPart(ctx, key, current, n, data)
		},
		tail: func(ctx context.Context, end int64, data []byte) error {
			return s.putObjectBytes(ctx, tailKey(key, st.B+end), data)
		},
	}

	res, err := cutter.cut(ctx, buf, tailLen+1, r)
	if err != nil {
		return fmt.Errorf("s3: append to %s: %w", key, err)
	}

	next := s3UploadState{U: uploadID, P: st.P + res.parts, B: st.B + res.partBytes}

	if err := commit(ctx, next.String(), res.total-int64(tailLen)); err != nil {
		return err
	}

	// The position moved on, so the tail it superseded is garbage; whatever
	// this misses is swept with the upload.
	if tailLen > 0 {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadCleanupTimeout)
		defer cancel()

		_ = s.deleteObject(cleanup, tailKey(key, at.Offset))
	}

	return nil
}

// loadTail reads the tail of the upload (the bytes after its parts, ending at
// offset) into dst, which has exactly its length.
func (s *S3BlobStore) loadTail(ctx context.Context, key string, offset int64, dst []byte) error {
	if len(dst) == 0 {
		return nil
	}

	tail := tailKey(key, offset)

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(tail)})
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("%w: %s", ErrUploadDataLost, tail)
		}

		return fmt.Errorf("s3: get %s: %w", tail, err)
	}

	defer func() { _ = out.Body.Close() }()

	if out.ContentLength == nil || *out.ContentLength != int64(len(dst)) {
		return fmt.Errorf("%w: %s is not %d bytes", ErrUploadDataLost, tail, len(dst))
	}

	if _, err := io.ReadFull(out.Body, dst); err != nil {
		return fmt.Errorf("s3: read %s: %w", tail, err)
	}

	return nil
}

// createUpload starts the multipart upload of an upload object.
func (s *S3BlobStore) createUpload(ctx context.Context, key string) (string, error) {
	out, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("s3: create multipart upload %s: %w", key, err)
	}

	return aws.ToString(out.UploadId), nil
}

// uploadPart sends one part. Sending a number again replaces it, which is what
// makes the retry of a failed append safe.
func (s *S3BlobStore) uploadPart(ctx context.Context, key, uploadID string, number int, data []byte) error {
	_, err := s.client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		UploadId:      aws.String(uploadID),
		PartNumber:    aws.Int32(partNumber(number)),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	}, withoutDefaultChecksum)
	if err != nil {
		if isNoSuchUpload(err) {
			return fmt.Errorf("%w: the multipart upload of %s is gone", ErrUploadDataLost, key)
		}

		return fmt.Errorf("s3: upload part %d of %s: %w", number, key, err)
	}

	return nil
}

// putObjectBytes stores data as one object.
func (s *S3BlobStore) putObjectBytes(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	}, withoutDefaultChecksum)
	if err != nil {
		return fmt.Errorf("s3: put %s: %w", key, err)
	}

	return nil
}

// withoutDefaultChecksum drops the CRC32 pass the SDK adds to every upload
// by default: the bodies are memory buffers whose digest the caller verifies
// end to end.
func withoutDefaultChecksum(o *s3.Options) {
	o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
}

// deleteObject removes one object; a missing one is fine.
func (s *S3BlobStore) deleteObject(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}

	return nil
}

// CompleteUpload implements UploadStore. An upload that never reached a part
// is its tail, copied to the upload key; otherwise the tail becomes the last
// part and the multipart upload is completed, which is repeatable: once it is
// gone, the finished object answers.
func (s *S3BlobStore) CompleteUpload(ctx context.Context, id string, at UploadPosition) (StagedObject, error) {
	key, err := uploadKey(id)
	if err != nil {
		return StagedObject{}, err
	}

	st, err := parseUploadState(at.State, at.Offset)
	if err != nil {
		return StagedObject{}, err
	}

	if st.U == "" {
		return s.completeFromTail(ctx, key, at.Offset)
	}

	obj, err := s.completeParts(ctx, key, st, at.Offset)
	if errors.Is(err, errUploadFinished) {
		return s.finishedUpload(ctx, key, at.Offset)
	}

	return obj, err
}

// errUploadFinished is the multipart upload of an upload being gone.
var errUploadFinished = errors.New("the multipart upload is gone")

// completeFromTail makes an upload without parts (at most a tail) an object.
func (s *S3BlobStore) completeFromTail(ctx context.Context, key string, offset int64) (StagedObject, error) {
	if offset == 0 {
		if err := s.putObjectBytes(ctx, key, nil); err != nil {
			return StagedObject{}, err
		}

		return s.finishedUpload(ctx, key, 0)
	}

	tail := tailKey(key, offset)

	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(key),
		CopySource: aws.String(s.bucket + "/" + url.PathEscape(tail)),
	})
	if err != nil {
		// CopyObject reports a missing source as a plain API error, not as
		// the typed NoSuchKey the Get and Head calls return.
		if isNotFound(err) || hasS3Code(err, "NoSuchKey") {
			return StagedObject{}, fmt.Errorf("%w: %s", ErrUploadDataLost, tail)
		}

		return StagedObject{}, fmt.Errorf("s3: copy %s to %s: %w", tail, key, err)
	}

	return s.finishedUpload(ctx, key, offset)
}

// finishedUpload describes the completed upload object and checks that it
// holds the recorded number of bytes.
func (s *S3BlobStore) finishedUpload(ctx context.Context, key string, offset int64) (StagedObject, error) {
	obj, err := s.statObject(ctx, key)
	if err != nil {
		if errors.Is(err, ErrStagingNotFound) {
			return StagedObject{}, fmt.Errorf("%w: %s", ErrUploadDataLost, key)
		}

		return StagedObject{}, err
	}

	if obj.Size != offset {
		return StagedObject{}, fmt.Errorf("%w: %s holds %d of %d bytes", ErrUploadDataLost, key, obj.Size, offset)
	}

	return obj, nil
}

// completeParts sends the tail as the last part, checks the parts against the
// position and completes the multipart upload. errUploadFinished when the
// multipart upload does not exist (any more).
func (s *S3BlobStore) completeParts(ctx context.Context, key string, st s3UploadState, offset int64) (StagedObject, error) {
	count := st.P

	if tailLen := offset - st.B; tailLen > 0 {
		data := make([]byte, tailLen)

		if err := s.loadTail(ctx, key, offset, data); err != nil {
			return StagedObject{}, err
		}

		count++
		if count > MaxParts {
			return StagedObject{}, ErrUploadTooLarge
		}

		if err := s.uploadPartOf(ctx, key, st.U, count, data); err != nil {
			return StagedObject{}, err
		}
	}

	parts, err := s.listParts(ctx, key, st.U)
	if err != nil {
		if isNoSuchUpload(err) {
			return StagedObject{}, errUploadFinished
		}

		return StagedObject{}, err
	}

	completed, err := uploadParts(parts, count, offset)
	if err != nil {
		return StagedObject{}, err
	}

	out, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(st.U),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		if isNoSuchUpload(err) {
			return StagedObject{}, errUploadFinished
		}

		return StagedObject{}, fmt.Errorf("s3: complete multipart upload %s: %w", key, err)
	}

	if out.ETag != nil && *out.ETag != "" {
		return StagedObject{Size: offset, Version: *out.ETag}, nil
	}

	return s.finishedUpload(ctx, key, offset)
}

// uploadPartOf is uploadPart reporting a vanished multipart upload as
// errUploadFinished: a repeated completion finds it assembled.
func (s *S3BlobStore) uploadPartOf(ctx context.Context, key, uploadID string, number int, data []byte) error {
	err := s.uploadPart(ctx, key, uploadID, number, data)
	if errors.Is(err, ErrUploadDataLost) {
		return errUploadFinished
	}

	return err
}

// uploadParts checks that parts 1..count are all there and add up to offset
// bytes (parts beyond count are leftovers of a failed append and are left
// out), and returns them as the part list of CompleteMultipartUpload.
func uploadParts(parts []types.Part, count int, offset int64) ([]types.CompletedPart, error) {
	have := make(map[int32]types.Part, len(parts))

	for _, p := range parts {
		if p.PartNumber != nil {
			have[*p.PartNumber] = p
		}
	}

	var sum int64

	completed := make([]types.CompletedPart, 0, count)

	for n := 1; n <= count; n++ {
		p, ok := have[partNumber(n)]
		if !ok || p.Size == nil || p.ETag == nil {
			return nil, fmt.Errorf("%w: part %d is missing", ErrUploadDataLost, n)
		}

		sum += *p.Size

		completed = append(completed, types.CompletedPart{ETag: p.ETag, PartNumber: aws.Int32(partNumber(n))})
	}

	if sum != offset {
		return nil, fmt.Errorf("%w: the parts hold %d of %d bytes", ErrUploadDataLost, sum, offset)
	}

	return completed, nil
}

// PromoteUpload implements UploadStore.
func (s *S3BlobStore) PromoteUpload(ctx context.Context, id, sha256, version string, metadata map[string]string) error {
	key, err := uploadKey(id)
	if err != nil {
		return err
	}

	return s.promoteObject(ctx, key, sha256, version, metadata)
}

// OpenUpload implements UploadStore. The SDK checks the CRC32 of a download
// against the checksum header of the object, which S3-compatible stores (Garage)
// report for a multipart object as that of one part; the digest of the blob is
// verified by the caller anyway.
func (s *S3BlobStore) OpenUpload(ctx context.Context, id, version string) (io.ReadCloser, error) {
	key, err := uploadKey(id)
	if err != nil {
		return nil, err
	}

	return s.openVersioned(ctx, key, version, func(o *s3.Options) {
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
}

// DeleteUpload implements UploadStore: the object, the tails and every
// unfinished multipart upload of the key.
func (s *S3BlobStore) DeleteUpload(ctx context.Context, id string) error {
	key, err := uploadKey(id)
	if err != nil {
		return err
	}

	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(key),
	})

	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("s3: list %s: %w", key, err)
		}

		for _, obj := range page.Contents {
			// Ids have one length, so only the object and its tails share the prefix.
			if obj.Key == nil || (*obj.Key != key && !strings.HasPrefix(*obj.Key, key+".")) {
				continue
			}

			if err := s.deleteObject(ctx, *obj.Key); err != nil {
				return err
			}
		}
	}

	return s.abortKeyMultipart(ctx, key)
}

// WalkUploads implements UploadStore: the objects (the upload, its tails, a
// chunk of the previous layout) and the unfinished multipart uploads under the
// upload prefix.
func (s *S3BlobStore) WalkUploads(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	if err := s.walkObjectIDs(ctx, uploadPrefix, olderThan, uploadObjectID, fn); err != nil {
		return err
	}

	ids, err := s.multipartIDs(ctx, uploadPrefix, olderThan, uploadObjectID)
	if err != nil || len(ids) == 0 {
		return err
	}

	return fn(ids)
}

// uploadObjectID is the upload an object key belongs to (uploads/<id> or one of
// its tails uploads/<id>.<offset>), if it is one.
func uploadObjectID(key string) (string, bool) {
	id, _, _ := strings.Cut(strings.TrimPrefix(key, uploadPrefix), ".")

	return id, validUploadID(id) == nil
}
