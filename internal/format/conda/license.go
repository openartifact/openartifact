// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"encoding/json"
	"io"
	"path"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// maxIndexJSONBytes bounds how much of info/index.json is read.
const maxIndexJSONBytes = 1 << 20

// declaredLicense returns the license declared in the package's info/index.json
// ("license"; "license_family" is not a license). Any problem reading the
// archive is treated as an undeclared license.
func declaredLicense(filename string, data []byte) string {
	var index []byte

	switch {
	case strings.HasSuffix(filename, ".tar.bz2"):
		index = indexFromTar(bzip2.NewReader(bytes.NewReader(data)))
	case strings.HasSuffix(filename, ".conda"):
		index = indexFromConda(data)
	}

	var meta struct {
		License string `json:"license"`
	}

	if json.Unmarshal(index, &meta) != nil {
		return ""
	}

	return strings.TrimSpace(meta.License)
}

// indexFromConda reads info/index.json from the info-*.tar.zst member of a
// .conda (zip) archive.
func indexFromConda(data []byte) []byte {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}

	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "info-") || !strings.HasSuffix(f.Name, ".tar.zst") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil
		}

		defer func() { _ = rc.Close() }()

		dec, err := zstd.NewReader(rc)
		if err != nil {
			return nil
		}

		defer dec.Close()

		return indexFromTar(dec)
	}

	return nil
}

func indexFromTar(r io.Reader) []byte {
	tr := tar.NewReader(r)

	for {
		hdr, err := tr.Next()
		if err != nil {
			return nil
		}

		if path.Clean(hdr.Name) != "info/index.json" {
			continue
		}

		b, err := io.ReadAll(io.LimitReader(tr, maxIndexJSONBytes))
		if err != nil {
			return nil
		}

		return b
	}
}
