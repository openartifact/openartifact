// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// errGemYanked is what a failed-verification cleanup reports once the gem was
// hidden: RubyGems hosting has no hard delete, so the record stays.
var errGemYanked = errors.New("RubyGems has no hard delete: the gem was yanked instead, which hides it from clients but keeps the version recorded until an administrator purges it")

// Gems are pushed as the raw .gem; the server reads name, version and platform
// out of the gem's own metadata. Only the gem files move: the specs indexes and
// the quick/ gemspecs are generated from what is stored.
//
// Yanked state is not carried over because no source records it: Nexus's
// RubyGems documentation knows no yank at all (a gem is simply deleted), and
// Artifactory's gem API accepts the yank command but its documentation
// describes no marker, property or listing for yanked gems; the AQL and
// asset listings the importer reads show only files that still exist. A
// yanked gem is therefore absent from the listing, and what is listed is
// live and is imported live. The importer never un-yanks: OpenArtifact has no
// unyank route and a push of a yanked version is a conflict.
func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format: formatRubyGems, nexus: []string{"rubygems"}, artifactory: []string{"gems"},
		importable: importableGem, match: matchSHA, present: presentByDigestOrFile,
		upload: gemUpload,
		remove: gemRemove,
	})
}

func importableGem(p string) bool {
	return strings.HasSuffix(p, ".gem")
}

func gemUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	hdr := r.Meta.header()
	hdr.Set("Content-Type", "application/octet-stream")

	length := r.Asset.Size
	if length <= 0 {
		length = -1
	}

	resp, err := t.send(ctx, http.MethodPost, "/repos/"+url.PathEscape(r.Dst)+"/api/v1/gems", r.Body, length, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return uploadResult{}, fmt.Errorf("POST gems: decoding response: %w", err)
	}

	// The response names the gem but neither its platform nor a digest; the
	// listing has both. The platform is what follows name and version in the
	// file name, which is how RubyGems itself writes it (and how the listing
	// spells it). A source file name that does not follow that scheme leaves
	// the platform open, which is only ambiguous when one version exists for
	// several platforms.
	platform := gemPlatform(path.Base(r.Asset.Path), out.Name, out.Version)

	return t.storedResult(ctx, r.Dst, formatRubyGems, out.Name, func(p listedPackage) bool {
		return p.Name == out.Name && p.Version == out.Version &&
			(platform == "" || gemPlatform(p.Filename, p.Name, p.Version) == platform)
	})
}

// gemPlatform reads the platform from a gem file name
// <name>-<version>[-<platform>].gem: "ruby" when nothing follows the version.
// It is "" (unknown) when the file name does not follow that scheme.
func gemPlatform(file, name, version string) string {
	rest, ok := strings.CutPrefix(strings.TrimSuffix(file, ".gem"), name+"-"+version)

	switch {
	case !ok:
		return ""
	case rest == "":
		return "ruby"
	}

	if platform, ok := strings.CutPrefix(rest, "-"); ok {
		return platform
	}

	return ""
}

// gemRemove takes the gem the upload created out of service. The API has no
// delete, only yank (hidden from the indexes, downloads and search, but still
// recorded), so it yanks and reports that it did.
func gemRemove(ctx context.Context, t *importTarget, dst string, _ sourceAsset, res uploadResult) error {
	p, ok, err := t.findStored(ctx, dst, formatRubyGems, "", func(p listedPackage) bool { return p.ID == res.ID })
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("%w: gem %q is not listed in %q", errNotFound, res.ID, dst)
	}

	// Without a platform the yank would hide every platform of the version.
	platform := gemPlatform(p.Filename, p.Name, p.Version)
	if platform == "" {
		return fmt.Errorf("%w: cannot tell the platform of gem %q, not yanking", errImportVerify, p.Filename)
	}

	form := url.Values{"gem_name": {p.Name}, "version": {p.Version}, "platform": {platform}}.Encode()

	apiPath := "/repos/" + url.PathEscape(dst) + "/api/v1/gems/yank"

	// Not send: a 409 here means immutable releases, not an upload conflict.
	resp, err := t.request(ctx, http.MethodDelete, apiPath, strings.NewReader(form), int64(len(form)),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return serverError(resp, "DELETE "+apiPath)
	}

	return errGemYanked
}
