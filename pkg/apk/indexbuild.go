// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // verifying the SHA-1 .SIGN.RSA. signatures of official Alpine indexes
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// sigRSA256Prefix names a SHA-256 signature entry, sigRSAPrefix the older
	// SHA-1 one.
	sigRSA256Prefix = ".SIGN.RSA256."
	sigRSAPrefix    = ".SIGN.RSA."

	// gzipOSUnix is the OS byte abuild's gzip writes.
	gzipOSUnix = 3

	// maxSignatureSegment and maxSignature bound the leading signature member
	// VerifyIndex reads: a 16384-bit RSA signature is 2048 bytes.
	maxSignatureSegment = 64 << 10
	maxSignature        = 4096
)

// ErrSignature is wrapped by every error VerifyIndex returns.
var ErrSignature = errors.New("apk index signature check failed")

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

// VerifyIndex verifies the signature in front of an APKINDEX.tar.gz and
// returns the name of the key that signed it. data must start with a gzip
// member whose tar holds one .SIGN.RSA256.<key name> (SHA-256) or
// .SIGN.RSA.<key name> (SHA-1) entry; the signature covers every byte after
// that member. keys maps key names, as in /etc/apk/keys, to public keys. Errors
// wrap [ErrSignature].
func VerifyIndex(data []byte, keys map[string]*rsa.PublicKey) (string, error) {
	name, sig, end, err := readSignature(data)
	if err != nil {
		return "", err
	}

	signed := data[end:]
	if len(signed) == 0 {
		return "", sigError("nothing follows the signature")
	}

	var (
		algorithm crypto.Hash
		digest    []byte
		keyName   string
	)

	switch {
	case strings.HasPrefix(name, sigRSA256Prefix):
		algorithm, keyName = crypto.SHA256, strings.TrimPrefix(name, sigRSA256Prefix)
		sum := sha256.Sum256(signed)
		digest = sum[:]
	case strings.HasPrefix(name, sigRSAPrefix):
		algorithm, keyName = crypto.SHA1, strings.TrimPrefix(name, sigRSAPrefix)
		sum := sha1.Sum(signed) //nolint:gosec // see the import
		digest = sum[:]
	default:
		return "", sigError("unsupported signature %q", short(name))
	}

	pub := keys[keyName]
	if keyName == "" || pub == nil {
		return "", sigError("no trusted key %q", short(keyName))
	}

	if err := rsa.VerifyPKCS1v15(pub, algorithm, digest, sig); err != nil {
		return "", sigError("key %q: %v", short(keyName), err)
	}

	return keyName, nil
}

func sigError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSignature, fmt.Sprintf(format, args...))
}

// readSignature reads the gzip member at the start of data, which must hold
// exactly one signature entry. It returns the entry name, the signature and
// the offset after the member.
func readSignature(data []byte) (string, []byte, int, error) {
	rd := bytes.NewReader(data)
	br := bufio.NewReader(rd)

	gz, err := newMemberReader(br)
	if err != nil {
		return "", nil, 0, sigError("the index does not start with a gzip member: %v", err)
	}

	raw, err := io.ReadAll(io.LimitReader(gz, maxSignatureSegment+1))
	if err != nil || len(raw) > maxSignatureSegment {
		return "", nil, 0, sigError("the signature member is unreadable or too large")
	}

	end := len(data) - rd.Len() - br.Buffered()
	tr := tar.NewReader(bytes.NewReader(raw))

	hdr, err := tr.Next()
	if err != nil || !strings.HasPrefix(hdr.Name, sigPrefix) {
		return "", nil, 0, sigError("the index is not signed")
	}

	if hdr.Typeflag != tar.TypeReg || hdr.Size > maxSignature {
		return "", nil, 0, sigError("malformed signature entry")
	}

	sig, err := io.ReadAll(tr)
	if err != nil {
		return "", nil, 0, sigError("reading the signature: %v", err)
	}

	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return "", nil, 0, sigError("the signature member holds more than one entry")
	}

	return hdr.Name, sig, end, nil
}
