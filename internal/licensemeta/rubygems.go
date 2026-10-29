// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/format/rubygems"
)

// RubyGemsHooks reads the licenses of rubygems from the gem's own metadata
// parser, not through the manifest package.
var RubyGemsHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	Expected: expectSupported,
	Read:     readGem,
}

// readGem reads the licenses of a .gem from the gemspec in its metadata.gz.
// It goes through the rubygems reader the upload path uses, not the manifest
// extractor: a gem written by this registry's own tooling carries a binary
// Ruby Marshal dump, which text extraction would corrupt, and a stored gem must
// yield exactly the licenses its upload was judged on.
func readGem(ctx context.Context, open func() (io.ReadCloser, error)) (Result, error) {
	rc, err := open()
	if err != nil {
		return Result{}, fmt.Errorf("open gem: %w", err)
	}

	defer func() { _ = rc.Close() }()

	declared, err := rubygems.ParseLicenses(ctx, rc)
	if errors.Is(err, rubygems.ErrMetadataTooLarge) {
		return Result{}, fmt.Errorf("%w: %w", ErrUnsafe, err)
	}

	if err != nil {
		return Result{}, fmt.Errorf("read gem: %w", err)
	}

	res := Result{Source: "gemspec"}

	for _, l := range declared {
		if l = strings.TrimSpace(l); l != "" {
			res.Licenses = append(res.Licenses, l)
		}
	}

	return res, nil
}
