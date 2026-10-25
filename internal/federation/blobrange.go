// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errBadRange is a Range header the blob endpoint cannot satisfy: a unit other
// than bytes, several ranges, a suffix range, malformed numbers, or a start at
// or past the end of the blob.
var errBadRange = errors.New("range not satisfiable")

// blobSpan is the part of a blob a request asks for; end is inclusive.
type blobSpan struct {
	start, end int64
	// partial is true when the request carried a Range and the answer is a 206.
	partial bool
}

// parseRange reads the Range header of a blob request against the blob's size.
// An absent header, or an unknown size, selects the whole blob. Only a single
// "bytes=N-" or "bytes=N-M" range is served; an end past the last byte is
// clamped to it.
func parseRange(header string, size int64) (blobSpan, error) {
	if header == "" || size <= 0 {
		return blobSpan{}, nil
	}

	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return blobSpan{}, errBadRange
	}

	first, last, ok := strings.Cut(spec, "-")
	if !ok || first == "" {
		return blobSpan{}, errBadRange // a suffix range (bytes=-N) is not served
	}

	start, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil || start < 0 || start >= size {
		return blobSpan{}, errBadRange
	}

	end := size - 1

	if last = strings.TrimSpace(last); last != "" {
		req, err := strconv.ParseInt(last, 10, 64)
		if err != nil || req < start {
			return blobSpan{}, errBadRange
		}

		end = min(req, end)
	}

	return blobSpan{start: start, end: end, partial: true}, nil
}

// contentRange is a parsed "bytes start-end/total" response header.
type contentRange struct {
	start, end, total int64
}

// parseContentRange reads the Content-Range header of a 206 answer.
func parseContentRange(header string) (contentRange, error) {
	var cr contentRange

	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes ")
	if !ok {
		return cr, fmt.Errorf("%w: %q", errBadContentRange, header)
	}

	span, total, ok := strings.Cut(spec, "/")
	if !ok {
		return cr, fmt.Errorf("%w: %q", errBadContentRange, header)
	}

	first, last, ok := strings.Cut(span, "-")
	if !ok {
		return cr, fmt.Errorf("%w: %q", errBadContentRange, header)
	}

	var err error

	if cr.start, err = strconv.ParseInt(first, 10, 64); err != nil {
		return cr, fmt.Errorf("%w: %q", errBadContentRange, header)
	}

	if cr.end, err = strconv.ParseInt(last, 10, 64); err != nil || cr.end < cr.start {
		return cr, fmt.Errorf("%w: %q", errBadContentRange, header)
	}

	// The complete length may be unknown ("*").
	if total == "*" {
		cr.total = -1
	} else if cr.total, err = strconv.ParseInt(total, 10, 64); err != nil {
		return cr, fmt.Errorf("%w: %q", errBadContentRange, header)
	}

	return cr, nil
}
