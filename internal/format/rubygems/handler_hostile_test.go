// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"testing"
)

// gemWithMetadata wraps raw (already decompressed) metadata bytes in the
// metadata.gz member of a minimal .gem tar.
func gemWithMetadata(t *testing.T, raw []byte) []byte {
	t.Helper()

	var meta bytes.Buffer

	gw := gzip.NewWriter(&meta)
	_, _ = gw.Write(raw)
	_ = gw.Close()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "metadata.gz", Mode: 0o644, Size: int64(meta.Len())})
	_, _ = tw.Write(meta.Bytes())
	_ = tw.Close()

	return buf.Bytes()
}

// TestRubyGemsHandler_Parse_DeeplyNestedMarshalMetadata uploads a gem whose
// metadata.gz is a Marshal stream of maxMetadataBytes of 'I' bytes, each
// opening one more nesting level. Before the decoder's depth limit this
// overflowed the goroutine stack: a fatal error that kills the whole server,
// not just the request. The gem must parse, falling back to raw metadata.
func TestRubyGemsHandler_Parse_DeeplyNestedMarshalMetadata(t *testing.T) {
	hostile := append([]byte{4, 8}, bytes.Repeat([]byte{'I'}, maxMetadataBytes-2)...)

	result, err := NewRubyGemsHandler().Parse(context.Background(), bytes.NewReader(gemWithMetadata(t, hostile)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if _, ok := result["raw_metadata"]; !ok {
		t.Errorf("Parse result = %v, want the raw_metadata fallback", result)
	}
}
