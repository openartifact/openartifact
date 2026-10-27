// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"time"
)

const (
	// sigRSA256Prefix names the SHA-256 signature entry of an index.
	sigRSA256Prefix = ".SIGN.RSA256."

	// gzipOSUnix is the OS byte abuild's gzip writes.
	gzipOSUnix = 3
)

type archiveFile struct {
	name string
	data []byte
}

// gzipTar returns the files as one gzip member holding a tar stream. The
// output is a function of the arguments alone: gzip mtime 0 and no name, tar
// entries with mtime 0, mode 0644, owner 0, plain ustar headers (never PAX).
// withEnd appends the end-of-archive blocks; without them the tar can be
// continued by the next member, which is how abuild writes signatures.
func gzipTar(files []archiveFile, withEnd bool) ([]byte, error) {
	var buf bytes.Buffer

	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}

	gw.OS = gzipOSUnix
	tw := tar.NewWriter(gw)

	for _, f := range files {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     f.name,
			Size:     int64(len(f.data)),
			Mode:     0o644,
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}

		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}

	if withEnd {
		err = tw.Close()
	} else {
		err = tw.Flush()
	}

	if err != nil {
		return nil, err
	}

	if err := gw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// BuildIndex builds a deterministic APKINDEX.tar.gz for pkgs: a gzip member
// with the tar entries DESCRIPTION and APKINDEX, ended like apk's own index
// tars. With a signer it is preceded by the signature member, a gzip member
// whose tar (no end-of-archive blocks, so that apk reads on into the next
// member) holds the entry .SIGN.RSA256.<key name>: the RSA PKCS#1 v1.5
// SHA-256 signature of the complete bytes of the index member. Every package
// must be valid (see [Package]), carrying at least the Checksum; errors wrap
// [ErrInvalidIndex].
func BuildIndex(pkgs []Package, description string, s *Signer) ([]byte, error) {
	if len(description) > maxTextLen || !cleanText(description) {
		return nil, badIndex("description is longer than %d bytes or has control characters", maxTextLen)
	}

	for i := range pkgs {
		if err := pkgs[i].validate(); err != nil {
			return nil, badIndex("package %d: %v", i+1, err)
		}
	}

	index, err := gzipTar([]archiveFile{
		{name: descFileName, data: []byte(description)},
		{name: indexFileName, data: RenderIndex(pkgs)},
	}, true)
	if err != nil {
		return nil, fmt.Errorf("building the index: %w", err)
	}

	if s == nil {
		return index, nil
	}

	sig, err := s.Sign(index)
	if err != nil {
		return nil, fmt.Errorf("signing the index: %w", err)
	}

	member, err := gzipTar([]archiveFile{{name: sigRSA256Prefix + s.name, data: sig}}, false)
	if err != nil {
		return nil, fmt.Errorf("building the signature: %w", err)
	}

	return append(member, index...), nil
}
