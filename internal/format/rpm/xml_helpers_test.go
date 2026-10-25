// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"io"

	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// renderXML runs one of the Write*XML streamers into memory.
func renderXML(write func(io.Writer, []pkgrpm.Package) error, pkgs []pkgrpm.Package) ([]byte, error) {
	var buf bytes.Buffer
	if err := write(&buf, pkgs); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// compressXML compresses an in-memory document through CompressMetadataStream.
func compressXML(data []byte) (*CompressedMetadata, error) {
	return CompressMetadataStream(func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}, len(data))
}
