// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Limits of a multipart upload, as enforced by the server.
const (
	minPartBytes int64 = 5 << 20
	maxPartBytes int64 = 4000 << 20
	maxPartCount       = 10000

	// maxPartsPerRequest is how many part URLs one request may ask for.
	maxPartsPerRequest = 100

	defaultUploadParallel = 4
	maxUploadParallel     = 16

	// partRetries is how often a failed part is sent again (three attempts in all).
	partRetries      = 2
	partRetryMaxWait = 30 * time.Second
)

var (
	errPartSize             = errors.New("invalid part size")
	errTooManyParts         = errors.New("too many parts")
	errMultipartUnsupported = errors.New("the server did not start a multipart upload (is it older than this CLI?)")
	errMultipartPlan        = errors.New("server returned an inconsistent multipart plan")
	errPartTarget           = errors.New("server returned an unusable part URL")
)

// parsePartSize parses a --part-size value: a byte count, or a whole number of
// MiB or GiB with a unit (128M, 128MiB, 1G, 1GiB), within the limits of a part.
func parsePartSize(s string) (int64, error) {
	s = strings.TrimSpace(s)

	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}

	n, err := strconv.ParseInt(s[:digits], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w %q: want a whole number of bytes, MiB or GiB (for example 128M or 1G)", errPartSize, s)
	}

	var unit int64

	switch strings.ToLower(s[digits:]) {
	case "":
		unit = 1
	case "m", "mib":
		unit = 1 << 20
	case "g", "gib":
		unit = 1 << 30
	default:
		return 0, fmt.Errorf("%w %q: unknown unit %q (use M or G)", errPartSize, s, s[digits:])
	}

	if n > maxPartBytes/unit || n*unit < minPartBytes {
		return 0, fmt.Errorf("%w %q: a part is %d MiB to %d MiB", errPartSize, s, minPartBytes>>20, maxPartBytes>>20)
	}

	return n * unit, nil
}

// partCount is the number of parts of size bytes split into partSize parts; an
// empty file still has one (empty) part.
func partCount(size, partSize int64) int {
	if size <= 0 || partSize <= 0 {
		return 1
	}

	return int((size + partSize - 1) / partSize)
}

// partLength is the exact size of part number (1-based).
func partLength(size, partSize int64, number int) int64 {
	if number < 1 || number > partCount(size, partSize) {
		return 0
	}

	return min(partSize, size-int64(number-1)*partSize)
}

// checkPartPlan rejects a requested part size that would need more parts than
// an upload may have, before the file is declared to the server. A zero part
// size leaves the choice to the server.
func checkPartPlan(size, partSize int64) error {
	if partSize <= 0 {
		return nil
	}

	if n := partCount(size, partSize); n > maxPartCount {
		return fmt.Errorf("%w: %d bytes in parts of %d bytes need %d parts, at most %d are allowed (use a larger --part-size)",
			errTooManyParts, size, partSize, n, maxPartCount)
	}

	return nil
}

// partTarget is the presigned PUT of one part.
type partTarget struct {
	url     string
	headers map[string]any
}

// multipartUpload sends the parts of one file to the part URLs the server
// issues. Part URLs are requested in batches as the upload proceeds, so they
// do not expire while earlier parts are still being sent; a part that is
// rejected with 403 gets a fresh URL. Parts are read straight from the file
// (io.SectionReader), so memory stays flat and a retry re-reads its range.
type multipartUpload struct {
	client   *Client
	file     io.ReaderAt
	base     string // API path of the upload, without a trailing slash
	size     int64
	partSize int64
	count    int
	parallel int
	retry    *retrier
	progress func(done, total int)

	// batch is how many part URLs one request asks for.
	batch int

	mu   sync.Mutex
	urls map[int]partTarget // issued ahead of use
	next int                // lowest part number not requested yet

	progressMu sync.Mutex
	done       int
}

// uploadParts sends the file in parts as planned by target. When it gives up
// the upload is aborted (best effort) so the server frees the staging space.
func (u *presignedUploader) uploadParts(file io.ReaderAt, size int64, base string, target uploadTarget, parallel int) error {
	uploadPath := base + "/" + escape(target.ID)

	err := checkMultipartTarget(size, target)
	if err == nil {
		m := &multipartUpload{
			client:   u.client,
			file:     file,
			base:     uploadPath,
			size:     size,
			partSize: target.PartSize,
			count:    target.PartCount,
			parallel: parallel,
			retry:    u.retry,
			progress: u.progress,
			batch:    min(maxPartsPerRequest, 4*parallel),
			urls:     map[int]partTarget{},
			next:     1,
		}

		err = m.run(context.Background())
	}

	if err != nil {
		u.abort(uploadPath + "/abort")

		return err
	}

	return nil
}

// checkMultipartTarget verifies the server started the multipart upload that
// was asked for, with a plan that covers the file.
func checkMultipartTarget(size int64, target uploadTarget) error {
	if !target.Multipart {
		return errMultipartUnsupported
	}

	if target.PartSize <= 0 || target.PartCount != partCount(size, target.PartSize) {
		return fmt.Errorf("%w: %d parts of %d bytes for %d bytes", errMultipartPlan, target.PartCount, target.PartSize, size)
	}

	return nil
}

// abort cancels an upload on the server. It is best effort: the CLI is already
// failing and the server expires stale uploads on its own.
func (u *presignedUploader) abort(path string) {
	resp, err := u.client.doJSON(http.MethodPost, path, nil)
	if err != nil {
		return
	}

	_ = resp.Body.Close()
}

// run sends every part, at most m.parallel at a time. The first part that fails
// for good stops the others.
func (m *multipartUpload) run(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(m.parallel)

	for n := 1; n <= m.count && ctx.Err() == nil; n++ {
		group.Go(func() error { return m.uploadPart(ctx, n) })
	}

	return group.Wait()
}

// uploadPart sends part n, retrying transient failures. A 403 from the object
// store means the part URL expired: a fresh one is requested and used at once.
func (m *multipartUpload) uploadPart(ctx context.Context, n int) error {
	offset := int64(n-1) * m.partSize
	length := partLength(m.size, m.partSize, n)

	var target *partTarget

	put := func() error {
		return putObject(ctx, m.client, target.url, target.headers, io.NewSectionReader(m.file, offset, length), length)
	}

	err := m.retry.do(ctx, "part "+strconv.Itoa(n), func(int) error {
		if target == nil {
			issued, err := m.target(n)
			if err != nil {
				return err
			}

			target = &issued
		}

		err := put()
		if se, ok := errors.AsType[*httpStatusError](err); ok && se.status == http.StatusForbidden {
			issued, ferr := m.freshTarget(n)
			if ferr != nil {
				return ferr
			}

			target = &issued
			err = put()
		}

		return err
	})
	if err != nil {
		return fmt.Errorf("part %d of %d: %w", n, m.count, err)
	}

	m.report()

	return nil
}

// report counts a finished part and tells the progress callback.
func (m *multipartUpload) report() {
	if m.progress == nil {
		return
	}

	m.progressMu.Lock()
	defer m.progressMu.Unlock()

	m.done++
	m.progress(m.done, m.count)
}

// target returns the URL of part n, requesting the next batch of URLs when it
// has not been issued yet.
func (m *multipartUpload) target(n int) (partTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if t, ok := m.urls[n]; ok {
		delete(m.urls, n)

		return t, nil
	}

	numbers := m.nextBatch(n)

	issued, err := m.requestParts(numbers)
	if err != nil {
		return partTarget{}, err
	}

	for number, t := range issued {
		if number != n {
			m.urls[number] = t
		}
	}

	m.next = max(m.next, numbers[len(numbers)-1]+1)

	return issued[n], nil
}

// nextBatch lists the part numbers to request when part n has no URL at hand:
// the next numbers not requested yet, so concurrent workers never ask for the
// same part twice. A part that was already issued once gets a request of its own.
func (m *multipartUpload) nextBatch(n int) []int {
	start := m.next

	switch {
	case n < start:
		return []int{n}
	case n >= start+m.batch:
		start = n
	}

	numbers := make([]int, 0, m.batch)
	for i := start; i <= min(start+m.batch-1, m.count); i++ {
		numbers = append(numbers, i)
	}

	return numbers
}

// freshTarget requests a new URL for part n, for when the one it holds expired.
func (m *multipartUpload) freshTarget(n int) (partTarget, error) {
	issued, err := m.requestParts([]int{n})
	if err != nil {
		return partTarget{}, err
	}

	return issued[n], nil
}

// requestParts asks the server for the URLs of the given part numbers and
// checks that each comes with the planned size.
func (m *multipartUpload) requestParts(numbers []int) (map[int]partTarget, error) {
	var out struct {
		Parts []struct {
			PartNumber int            `json:"part_number"`
			UploadURL  string         `json:"upload_url"`
			Headers    map[string]any `json:"headers"`
			Size       int64          `json:"size"`
		} `json:"parts"`
	}

	if err := postJSON(m.client, m.base+"/parts", map[string]any{"part_numbers": numbers}, &out, http.StatusOK); err != nil {
		return nil, fmt.Errorf("requesting part URLs: %w", err)
	}

	issued := make(map[int]partTarget, len(numbers))

	for _, p := range out.Parts {
		if p.UploadURL != "" && p.Size == partLength(m.size, m.partSize, p.PartNumber) {
			issued[p.PartNumber] = partTarget{url: p.UploadURL, headers: p.Headers}
		}
	}

	for _, n := range numbers {
		if _, ok := issued[n]; !ok {
			return nil, fmt.Errorf("%w: part %d", errPartTarget, n)
		}
	}

	return issued, nil
}
