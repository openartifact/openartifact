// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/pkg/nix"
)

// A store path of a Nix binary cache is two files: the NAR (nar/<file>) and the
// narinfo that names it (<hash>.narinfo). That is how `nix copy --to` publishes
// it, and what the importer does: the NAR, which is the asset's content, is PUT
// first, then the narinfo the source served, which commits the path.
//
// The narinfo travels in sourceAsset.Extra (key nixNarInfoKey) because it is
// metadata of the path, not a file of its own. It keeps the signatures the
// source's repository added: they stay valid for a client that trusts the old
// key, and the destination's repository signs the path with its own key as well.
// Only an OpenArtifact repository holds nix binary caches.
func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format:     formatNix,
		importable: func(p string) bool { return nixStoreHash(p) != "" },
		match:      matchSHA,
		upload:     nixUpload,
		remove: func(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
			return t.deletePath(ctx, "/repos/"+url.PathEscape(dst)+"/"+url.PathEscape(a.Path))
		},
	})
}

// nixNarInfoKey is the sourceAsset.Extra key that carries the text of the
// narinfo of a store path.
const nixNarInfoKey = "nix.narinfo"

// nixStoreHash is the store hash of a "<hash>.narinfo" path, "" for anything else.
func nixStoreHash(p string) string {
	h, ok := strings.CutSuffix(p, ".narinfo")
	if !ok || h == "" || strings.ContainsAny(h, "/.") {
		return ""
	}

	return h
}

func nixUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	hash := nixStoreHash(r.Asset.Path)

	text := r.Asset.Extra[nixNarInfoKey]
	if hash == "" || text == "" {
		return uploadResult{}, fmt.Errorf("%w: %s: the source gave no narinfo", errImportUsage, r.Asset.Path)
	}

	info, err := nix.ParseNarInfo([]byte(text))
	if err != nil {
		return uploadResult{}, fmt.Errorf("%w: %s: %w", errImportUsage, r.Asset.Path, err)
	}

	file, _, err := info.NarFile()
	if err != nil {
		return uploadResult{}, fmt.Errorf("%w: %s: %w", errImportUsage, r.Asset.Path, err)
	}

	repo := "/repos/" + url.PathEscape(r.Dst) + "/"

	hdr := r.Meta.header()
	hdr.Set("Content-Type", "application/x-nix-nar")

	length := r.Asset.Size
	if length <= 0 {
		length = -1
	}

	resp, err := t.send(ctx, http.MethodPut, repo+"nar/"+url.PathEscape(file), r.Body, length, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	_ = resp.Body.Close()

	hdr = r.Meta.header()
	hdr.Set("Content-Type", "text/x-nix-narinfo")

	resp, err = t.send(ctx, http.MethodPut, repo+url.PathEscape(hash)+".narinfo", strings.NewReader(text), int64(len(text)), hdr)
	if err != nil {
		return uploadResult{}, err
	}

	_ = resp.Body.Close()

	// The PUT answers with no body: the listing has the id and the digest.
	return t.storedResult(ctx, r.Dst, formatNix, hash, func(p listedPackage) bool { return p.Version == hash })
}
