// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// npmMaxPackument bounds the packument read from the source: only its
// dist-tags are used, but the whole document has to be decoded.
const npmMaxPackument = 256 << 20

var errNPMPackument = errors.New("cannot locate the packument of the package in the source")

// npmTransferUnit imports one package: its versions, oldest first, then the
// dist-tags the source holds. The tags come last because a tag may only point at
// a version the destination has.
func npmTransferUnit(ctx context.Context, im *importer, j importJob) {
	for _, a := range j.assets {
		if ctx.Err() != nil {
			return
		}

		im.handleAsset(ctx, j, a)
	}

	if ctx.Err() == nil && len(j.assets) > 0 {
		npmImportTags(ctx, im, j)
	}
}

// npmImportTags copies the source's dist-tags of the package of the unit. A
// tag the destination cannot take (its version was not imported, or the server
// refuses the tag) is reported as a failure of the packument; the other tags
// are still applied. Tags the destination already has are left alone.
func npmImportTags(ctx context.Context, im *importer, j importJob) {
	first := j.assets[0]
	name, _, _ := npmCoordinates(first.Path)

	where, rawURL, err := npmPackumentLocation(im.cfg.Kind, first, name)
	if err != nil {
		im.rep.fail(j.repo.Name, name, err)
		return
	}

	tags, err := npmSourceTags(ctx, im, where, rawURL)

	switch {
	case npmStatus(err) == http.StatusNotFound:
		im.rep.logf("no packument %s/%s in the source: only the derived latest tag is set", j.repo.Name, where)
		return
	case err != nil:
		im.rep.fail(j.repo.Name, where, err)
		return
	}

	if im.cfg.DryRun {
		for _, tag := range slices.Sorted(maps.Keys(tags)) {
			im.rep.logf("would tag %s %s -> %s", name, tag, tags[tag])
		}

		return
	}

	current, err := npmDistTags(ctx, im.tgt, j.dst, name)
	if err != nil {
		im.rep.fail(j.repo.Name, where, err)
		return
	}

	for _, tag := range slices.Sorted(maps.Keys(tags)) {
		version := tags[tag]
		if current[tag] == version {
			continue
		}

		if err := npmSetTag(ctx, im, j.dst, name, tag, version); err != nil {
			im.rep.fail(j.repo.Name, where, err)
			continue
		}

		im.rep.logf("tagged   %s %s -> %s", name, tag, version)
	}
}

// npmPackumentLocation says where the source keeps the package's packument: the
// path it is listed under and its URL, derived from a tarball of the package.
//
// Nexus serves the packument at the package name, next to the "-/" directory of
// the tarballs. Artifactory keeps it in a dot directory, which the payload
// listing leaves out, at .npm/<package>/package.json.
func npmPackumentLocation(kind string, tarball sourceAsset, name string) (string, string, error) {
	if kind == "artifactory" {
		where := ".npm/" + name + "/package.json"

		root, ok := strings.CutSuffix(tarball.URL, escapePath(tarball.Path))
		if !ok {
			return "", "", fmt.Errorf("%w: %s", errNPMPackument, name)
		}

		return where, root + escapePath(where), nil
	}

	i := strings.LastIndex(tarball.URL, "/-/")
	if i < 0 {
		return "", "", fmt.Errorf("%w: %s", errNPMPackument, name)
	}

	return name, tarball.URL[:i], nil
}

// npmSourceTags reads the dist-tags of a packument served by the source.
func npmSourceTags(ctx context.Context, im *importer, where, rawURL string) (map[string]string, error) {
	var doc struct {
		DistTags map[string]string `json:"dist-tags"` //nolint:tagliatelle // npm registry protocol
	}

	err := im.retry.do(ctx, "GET "+redactURL(rawURL), func(int) error {
		rc, err := im.src.Open(ctx, sourceAsset{Path: where, URL: rawURL})
		if err != nil {
			return err
		}

		defer func() { _ = rc.Close() }()

		doc.DistTags = nil

		if err := json.NewDecoder(io.LimitReader(rc, npmMaxPackument)).Decode(&doc); err != nil {
			return fmt.Errorf("decoding the packument %s: %w", where, err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return doc.DistTags, nil
}

// npmSetTag points a dist-tag of a package in the destination at a version.
func npmSetTag(ctx context.Context, im *importer, dst, name, tag, version string) error {
	body, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("dist-tag %s -> %s: %w", tag, version, err)
	}

	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")

	apiPath := "/repos/" + url.PathEscape(dst) + "/-/package/" + npmEscapedName(name) + "/dist-tags/" + url.PathEscape(tag)

	err = im.retry.do(ctx, "PUT "+apiPath, func(int) error {
		resp, err := im.tgt.send(ctx, http.MethodPut, apiPath, bytes.NewReader(body), int64(len(body)), hdr)
		if err != nil {
			return err
		}

		_ = resp.Body.Close()

		return nil
	})

	switch {
	case npmStatus(err) == http.StatusNotFound:
		return fmt.Errorf("dist-tag %s -> %s: the version is not in the destination (not imported, or its import failed): %w", tag, version, err)
	case err != nil:
		return fmt.Errorf("dist-tag %s -> %s: %w", tag, version, err)
	}

	return nil
}

// npmStatus is the HTTP status behind an error, 0 when it has none.
func npmStatus(err error) int {
	if se, ok := errors.AsType[*httpStatusError](err); ok {
		return se.status
	}

	return 0
}
