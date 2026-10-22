// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package xzutil builds xz writers with a dictionary sized to the input.
package xzutil

import (
	"fmt"
	"io"

	"github.com/ulikunitz/xz"
)

const (
	minDictCap = 4096
	// MaxDictCap is the library default dictionary size.
	MaxDictCap = 8 << 20
)

// NewWriter returns an xz writer whose dictionary is sized to size input bytes
// (floor 4 KiB, ceiling MaxDictCap). The default 8 MiB dictionary costs about
// 50 MB of allocations per writer whatever the input size. Pass MaxDictCap when
// the input size is unknown.
func NewWriter(w io.Writer, size int) (*xz.Writer, error) {
	cfg := xz.WriterConfig{DictCap: min(max(size, minDictCap), MaxDictCap)}

	xw, err := cfg.NewWriter(w)
	if err != nil {
		return nil, fmt.Errorf("xz writer: %w", err)
	}

	return xw, nil
}
