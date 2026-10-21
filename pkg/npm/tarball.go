// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // required by protocol specification
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	maxTarballSize     = 200 << 20 // 200 MiB
	maxPackageJSONSize = 4 << 20   // 4 MiB
)

var (
	ErrTarballTooLarge     = errors.New("tarball exceeds max size")
	ErrPackageJSONTooLarge = errors.New("package.json exceeds max size")
)

// readAllLimited reads at most max+1 bytes from r and errors if the
// result exceeds max, without ever allocating more than max+1 bytes.
func readAllLimited(r io.Reader, limit int64, tooLarge error) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: max %d bytes", tooLarge, limit)
	}

	return data, nil
}

// limitedReader reads from r and fails with ErrTarballTooLarge as soon as
// more than limit bytes have been consumed.
type limitedReader struct {
	r        io.Reader
	n        int64 // bytes still allowed
	exceeded bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.exceeded {
		return 0, ErrTarballTooLarge
	}

	// Allow one byte past the limit so that overflow is detectable.
	if int64(len(p)) > l.n+1 {
		p = p[:l.n+1]
	}

	n, err := l.r.Read(p)

	if int64(n) > l.n {
		l.exceeded = true
		n = int(l.n)
		l.n = 0

		return n, ErrTarballTooLarge
	}

	l.n -= int64(n)

	return n, err
}

// ParseTarball extracts package/package.json from an npm tarball and returns
// it together with the sha512 SRI integrity string and the sha1 shasum of the
// whole (compressed) tarball. The tarball is streamed: only package.json and
// fixed-size decompression buffers are held in memory.
func ParseTarball(r io.Reader) (map[string]any, string, string, error) {
	h1 := sha1.New() //nolint:gosec // required by protocol specification
	h512 := sha512.New()
	lr := &limitedReader{r: r, n: maxTarballSize}
	tee := io.TeeReader(lr, io.MultiWriter(h1, h512))

	manifest, err := extractPackageJSON(tee)

	// The digests cover the entire tarball: consume whatever the
	// package.json scan did not need (this also enforces the size limit).
	if !lr.exceeded {
		if _, derr := io.Copy(io.Discard, tee); derr != nil && !lr.exceeded {
			return nil, "", "", fmt.Errorf("reading tarball: %w", derr)
		}
	}

	if lr.exceeded {
		return nil, "", "", fmt.Errorf("reading tarball: %w: max %d bytes", ErrTarballTooLarge, int64(maxTarballSize))
	}

	if err != nil {
		return nil, "", "", err
	}

	var sum [sha512.Size]byte

	integrity := "sha512-" + base64.StdEncoding.EncodeToString(h512.Sum(sum[:0]))
	shasum := hex.EncodeToString(h1.Sum(nil))

	return manifest, integrity, shasum, nil
}

func extractPackageJSON(r io.Reader) (map[string]any, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip open: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}

		if hdr.Name == "package/package.json" {
			data, err := readAllLimited(tr, maxPackageJSONSize, ErrPackageJSONTooLarge)
			if err != nil {
				return nil, fmt.Errorf("read package.json: %w", err)
			}

			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				return nil, fmt.Errorf("decode package.json: %w", err)
			}

			return m, nil
		}
	}

	return nil, errors.New("tarball missing package/package.json") //nolint:err113 // static error
}
