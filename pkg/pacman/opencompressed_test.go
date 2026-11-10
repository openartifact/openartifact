// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/M0Rf30/pkgformat/xzlimit"
)

// TestOpenCompressedRejectsHugeXZDictionary: an uploaded package whose xz
// header declares a 4 GiB dictionary is refused before the decoder allocates it.
func TestOpenCompressedRejectsHugeXZDictionary(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = xw.Write([]byte("hello"))
	_ = xw.Close()

	raw := buf.Bytes()

	if _, comp, _, err := openCompressed(bytes.NewReader(raw)); err != nil || comp != CompressionXZ {
		t.Fatalf("valid xz: %q, %v", comp, err)
	}

	raw[16] = 0x28 // LZMA2 dictionary-size property: 4 GiB - 1

	_, _, closeFn, err := openCompressed(bytes.NewReader(raw))
	if closeFn != nil {
		closeFn()
	}

	if !errors.Is(err, xzlimit.ErrDictTooLarge) || !errors.Is(err, ErrNotPackage) {
		t.Fatalf("err = %v, want ErrDictTooLarge and ErrNotPackage", err)
	}
}
