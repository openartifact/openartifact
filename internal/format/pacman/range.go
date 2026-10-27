// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/iocopy"
)

// parseRange parses the one byte range pacman resumes a partial download with,
// "bytes=START-" (an END is accepted too), against a body of size bytes. ok is
// false for anything else, which is answered with the whole body as HTTP allows;
// unsatisfiable is true for a start beyond the end.
func parseRange(header string, size int64) (int64, int64, bool, bool) {
	spec, found := strings.CutPrefix(header, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, false
	}

	first, last, found := strings.Cut(spec, "-")
	if !found || first == "" {
		return 0, 0, false, false
	}

	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false, false
	}

	end := size - 1

	if last != "" {
		e, err := strconv.ParseInt(last, 10, 64)
		if err != nil || e < start {
			return 0, 0, false, false
		}

		end = min(e, end)
	}

	if start >= size {
		return 0, 0, false, true
	}

	return start, end, true, false
}

// writePackage streams a package body. The blob store reads sequentially, so a
// ranged request skips the bytes before the range.
func (h *HTTPHandler) writePackage(w http.ResponseWriter, r *http.Request, body io.Reader, size int64) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")

	start, end, ok, unsatisfiable := parseRange(r.Header.Get("Range"), size)

	switch {
	case unsatisfiable:
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)

		return
	case !ok:
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
	default:
		if _, err := iocopy.Copy(io.Discard, io.LimitReader(body, start)); err != nil {
			h.Log.Warnw("failed to skip to the range start", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		body = io.LimitReader(body, end-start+1)

		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	}

	if _, err := iocopy.Copy(w, body); err != nil {
		h.Log.Warnw("failed to write pacman package", "error", err)
	}
}
