// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"crypto/md5"  //nolint:gosec // required by protocol specification
	"crypto/sha1" //nolint:gosec // required by protocol specification
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"io"
)

// Checksum returns the hex digest of data for one Maven checksum file suffix
// (".md5", ".sha1" or ".sha512"), hashing with that algorithm only. Any other
// suffix returns "".
func Checksum(suffix string, data []byte) string {
	switch suffix {
	case ".md5":
		sum := md5.Sum(data) //nolint:gosec // required by protocol specification

		return hex.EncodeToString(sum[:])
	case ".sha1":
		sum := sha1.Sum(data) //nolint:gosec // required by protocol specification

		return hex.EncodeToString(sum[:])
	case ".sha512":
		sum := sha512.Sum512(data)

		return hex.EncodeToString(sum[:])
	default:
		return ""
	}
}

func ComputeChecksums(r io.Reader) (string, string, string, string, error) {
	hMD5 := md5.New()   //nolint:gosec // required by protocol specification
	hSHA1 := sha1.New() //nolint:gosec // required by protocol specification
	hSHA256 := sha256.New()
	hSHA512 := sha512.New()

	mw := io.MultiWriter(hMD5, hSHA1, hSHA256, hSHA512)
	if _, err := io.Copy(mw, r); err != nil {
		return "", "", "", "", err
	}

	return hex.EncodeToString(hMD5.Sum(nil)),
		hex.EncodeToString(hSHA1.Sum(nil)),
		hex.EncodeToString(hSHA256.Sum(nil)),
		hex.EncodeToString(hSHA512.Sum(nil)),
		nil
}
