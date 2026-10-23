// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// A yanked crate keeps its file but is hidden from new dependency resolution.
// The registry web API has no "yanked" flag at publish time, so the importer
// publishes every crate and then yanks the ones the source reports as yanked.
//
// What a source can tell:
//   - Artifactory keeps the sparse index of a local Cargo repository (JSON
//     lines with `yanked` and the crate's `cksum`). It is read through the
//     source's own authenticated client, from the index API endpoint
//     (api/cargo/<repo>/index/...) and, failing that, from index/... inside the
//     repository. Artifactory also records a yank as a property (the docs say
//     yanking is synchronised through properties), read from the asset listing.
//   - Nexus cannot host Cargo repositories, so a Nexus source never yanks.
//
// The yank state is applied after the crate is stored (uploaded or already
// present) and is idempotent; a crate is never unyanked.

const (
	// cargoMaxIndex bounds one crate's index file; a crate with thousands of
	// versions stays far below it.
	cargoMaxIndex = 16 << 20
	// cargoYankSuffix extends an asset's state key to record that its yank has
	// been applied.
	cargoYankSuffix = "\x00yanked"
)

var errCargoYankVersion = errors.New("cannot tell the crate version")

// cargoIndexLine is one version of a crate in the sparse index.
type cargoIndexLine struct {
	Name   string `json:"name"`
	Vers   string `json:"vers"`
	Cksum  string `json:"cksum"`
	Yanked bool   `json:"yanked"`
}

// cargoUnits groups the versions of one crate into one unit, so the crate's
// index file is read once and its versions are published in turn.
func cargoUnits(assets []sourceAsset) [][]sourceAsset {
	var (
		units [][]sourceAsset
		at    = map[string]int{}
	)

	for _, a := range assets {
		key := strings.ToLower(cargoCrateName(a.Path))

		i, ok := at[key]
		if !ok {
			i = len(units)
			at[key] = i
			units = append(units, nil)
		}

		units[i] = append(units[i], a)
	}

	return units
}

// cargoCrateName derives the crate name from a crate file path: the directory
// of Artifactory's layout (crates/<name>/<name>-<version>.crate), else the file
// name up to the first "-<digit>".
func cargoCrateName(p string) string {
	stem := strings.TrimSuffix(path.Base(p), ".crate")

	if dir := path.Base(path.Dir(p)); dir != "." && dir != "/" && len(stem) > len(dir)+1 && strings.HasPrefix(stem, dir+"-") {
		return dir
	}

	for i := 1; i+1 < len(stem); i++ {
		if stem[i] == '-' && stem[i+1] >= '0' && stem[i+1] <= '9' {
			return stem[:i]
		}
	}

	return stem
}

// cargoPathCoordinates is the (name, version) a crate file path names.
func cargoPathCoordinates(p string) (string, string) {
	stem := strings.TrimSuffix(path.Base(p), ".crate")
	name := cargoCrateName(p)

	vers, _ := strings.CutPrefix(stem, name+"-")
	if vers == stem {
		vers = ""
	}

	return name, vers
}

// cargoIndexPath is where the sparse index keeps a crate: 1/<n>, 2/<n>,
// 3/<first letter>/<n> and <first two>/<next two>/<n> for longer names.
func cargoIndexPath(name string) string {
	n := strings.ToLower(name)

	switch {
	case n == "":
		return ""
	case len(n) <= 2:
		return strconv.Itoa(len(n)) + "/" + n
	case len(n) == 3:
		return "3/" + n[:1] + "/" + n
	}

	return n[:2] + "/" + n[2:4] + "/" + n
}

// cargoIndexURLs lists where an Artifactory repository can serve the index
// file of a crate, derived from the URL of one of its crate files.
func cargoIndexURLs(a sourceAsset, rel string) []string {
	repoURL, ok := strings.CutSuffix(a.URL, "/"+escapePath(a.Path))
	if !ok {
		return nil
	}

	root, ok := strings.CutSuffix(repoURL, "/"+url.PathEscape(a.Repo))
	if !ok {
		return nil
	}

	rel = escapePath(rel)

	return []string{
		root + "/api/cargo/" + url.PathEscape(a.Repo) + "/index/" + rel,
		repoURL + "/index/" + rel,
	}
}

// cargoParseIndex reads the JSON lines of an index file.
func cargoParseIndex(r io.Reader) ([]cargoIndexLine, error) {
	var (
		lines []cargoIndexLine
		dec   = json.NewDecoder(io.LimitReader(r, cargoMaxIndex))
	)

	for dec.More() {
		var l cargoIndexLine
		if err := dec.Decode(&l); err != nil {
			// A truncated body also surfaces as an unexpected EOF, which the
			// retry policy would take for a network error.
			return nil, permanent(fmt.Errorf("decoding the index: %w", err))
		}

		lines = append(lines, l)
	}

	return lines, nil
}

// cargoReadIndex reads the index file of the crate the unit holds. It returns
// no lines when the source cannot tell (not Artifactory, no such file).
func cargoReadIndex(ctx context.Context, src importSource, sample sourceAsset) ([]cargoIndexLine, string, error) {
	as, ok := src.(*artifactorySource)
	if !ok {
		return nil, "", nil
	}

	rel := cargoIndexPath(cargoCrateName(sample.Path))
	if rel == "" {
		return nil, "", nil
	}

	for _, u := range cargoIndexURLs(sample, rel) {
		lines, err := as.cargoIndex(ctx, u)

		switch {
		case errors.Is(err, errNotFound):
			continue
		case err != nil:
			return nil, "index/" + rel, err
		}

		return lines, "index/" + rel, nil
	}

	return nil, "index/" + rel, nil
}

// cargoIndex GETs and parses one index file; a 404 is errNotFound.
func (a *artifactorySource) cargoIndex(ctx context.Context, rawURL string) ([]cargoIndexLine, error) {
	var lines []cargoIndexLine

	err := a.http.retry.do(ctx, "GET "+redactURL(rawURL), func(int) error {
		body, err := a.http.open(ctx, rawURL)
		if err != nil {
			var se *httpStatusError
			if errors.As(err, &se) && se.status == http.StatusNotFound {
				return errNotFound
			}

			return err
		}

		defer func() { _ = body.Close() }()

		lines, err = cargoParseIndex(body)

		return err
	})

	return lines, err
}

// cargoPropertyYanked reports whether Artifactory's properties mark the crate
// as yanked.
func cargoPropertyYanked(props map[string][]string) bool {
	for k, vs := range props {
		switch strings.ToLower(k) {
		case "cargo.yanked", "cargo.yank":
			for _, v := range vs {
				if strings.EqualFold(strings.TrimSpace(v), "true") {
					return true
				}
			}
		}
	}

	return false
}

// cargoYankState tells the coordinates of a crate file and whether the source
// has it yanked. The index line is found by the crate's checksum, else by its
// file name; without one the coordinates come from the path.
func cargoYankState(a sourceAsset, lines []cargoIndexLine) (string, string, bool) {
	name, vers := cargoPathCoordinates(a.Path)
	yanked := cargoPropertyYanked(a.Props)
	stem := strings.TrimSuffix(path.Base(a.Path), ".crate")

	for _, l := range lines {
		byFile := strings.EqualFold(l.Name+"-"+l.Vers, stem)
		bySum := a.SHA256 != "" && strings.EqualFold(l.Cksum, a.SHA256)

		if byFile || bySum {
			return l.Name, l.Vers, yanked || l.Yanked
		}
	}

	return name, vers, yanked
}

// cargoTransferUnit imports the versions of one crate and then applies the
// yank state the source reports. It reuses the importer's per-asset transfer
// (resume, present detection, dry run, upload, verification).
func cargoTransferUnit(ctx context.Context, im *importer, j importJob) {
	lines, rel, err := cargoReadIndex(ctx, im.src, j.assets[0])
	if err != nil {
		im.rep.fail(j.repo.Name, rel, fmt.Errorf("reading the yanked state of the crate: %w", err))
	}

	for _, a := range j.assets {
		if ctx.Err() != nil {
			return
		}

		im.handleAsset(ctx, j, a)
		cargoApplyYank(ctx, im, j, a, lines)
	}
}

// cargoApplyYank yanks a stored crate the source reports as yanked. A crate
// whose import failed is left alone (its failure is already reported).
func cargoApplyYank(ctx context.Context, im *importer, j importJob, a sourceAsset, lines []cargoIndexLine) {
	name, vers, yanked := cargoYankState(a, lines)
	if !yanked || cargoAssetFailed(im.rep, j.repo.Name, a.Path) {
		return
	}

	key := stateKey(j.repo.Name, j.dst, a.Path+cargoYankSuffix)
	marker := "yanked:" + resumeMarker(a)

	if im.state.isDone(key, marker) {
		return
	}

	if im.cfg.DryRun {
		im.rep.logf("would yank %s/%s -> %s", j.repo.Name, a.Path, j.dst)
		return
	}

	if err := cargoYank(ctx, im, j.dst, name, vers); err != nil {
		im.rep.fail(j.repo.Name, a.Path, fmt.Errorf("yanking: %w", err))
		return
	}

	im.complete(key, marker)
	im.rep.logf("yanked   %s/%s -> %s", j.repo.Name, a.Path, j.dst)
}

// cargoAssetFailed reports whether the run already failed this asset.
func cargoAssetFailed(r *importReport, repo, p string) bool {
	failed := false

	r.add(func(r *importReport) {
		for _, f := range r.Failures {
			if f.Repo == repo && f.Path == p {
				failed = true
				return
			}
		}
	})

	return failed
}

// cargoYank yanks one version through the registry web API. Yanking an already
// yanked version succeeds, so the request is repeated after transient failures.
func cargoYank(ctx context.Context, im *importer, dst, name, vers string) error {
	if name == "" || vers == "" {
		return errCargoYankVersion
	}

	p := "/repos/" + url.PathEscape(dst) + "/api/v1/crates/" + url.PathEscape(name) + "/" + url.PathEscape(vers) + "/yank"

	return im.retry.do(ctx, "yank "+name+" "+vers, func(int) error {
		return im.tgt.deletePath(ctx, p)
	})
}
