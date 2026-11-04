// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package xzlimit

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ulikunitz/xz"
)

func xzWithDict(t *testing.T, dictCap int) []byte {
	t.Helper()

	var buf bytes.Buffer

	w, err := xz.WriterConfig{DictCap: dictCap}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestCheckDictCap(t *testing.T) {
	if err := CheckDictCap(xzWithDict(t, 8<<20)); err != nil {
		t.Errorf("8 MiB dictionary rejected: %v", err)
	}

	err := CheckDictCap(xzWithDict(t, 512<<20))
	if !errors.Is(err, ErrDictTooLarge) {
		t.Errorf("512 MiB dictionary: err = %v, want ErrDictTooLarge", err)
	}

	// Truncated or non-xz input is left for the decoder to reject.
	if err := CheckDictCap([]byte("not xz")); err != nil {
		t.Errorf("garbage: %v", err)
	}
}

func TestLZMA2DictSize(t *testing.T) {
	cases := map[byte]int64{0: 4096, 1: 6144, 2: 8192, 18: 2 << 20, 22: 8 << 20, 24: 16 << 20, 40: 1<<32 - 1}
	for b, want := range cases {
		if got := lzma2DictSize(b); got != want {
			t.Errorf("lzma2DictSize(%d) = %d, want %d", b, got, want)
		}
	}
}
