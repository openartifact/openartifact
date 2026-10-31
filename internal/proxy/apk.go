// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
)

// An apk proxy mirrors an Alpine mirror root by path: the key of a file is
// `<branch>/<repo>/<arch>/<file>`, the layout apk's repositories line
// (https://HOST/repos/NAME/v3.20/main) expands to. Two kinds of file exist:
//
//   - APKINDEX.tar.gz, passed through byte for byte so the upstream signature
//     stays valid for the client, which checks it against the keys it trusts
//     (the proxy has no keys configured and never verifies it). It is cached
//     for the repository TTL like deb dists/ and rpm repodata.
//   - <name>-<version>.apk, cached as immutable once verified: the parsed
//     package must have the checksum the upstream index of the same directory
//     lists for that file name. A package the index does not list is a 404, one
//     that does not match it or is not a valid package is refused (502) and
//     never cached.
//
// Every other path is answered 404 without contacting the upstream.

const (
	apkIndexName = "APKINDEX.tar.gz"
	apkKeySegs   = 4
)

// apkIndexKey reports whether key is the index of a `<branch>/<repo>/<arch>`
// directory.
func apkIndexKey(key string) bool {
	return strings.Count(key, "/") == apkKeySegs-1 && strings.HasSuffix(key, "/"+apkIndexName)
}

// apkPackageKey reports whether key is a package file of a
// `<branch>/<repo>/<arch>` directory.
func apkPackageKey(key string) bool {
	if strings.Count(key, "/") != apkKeySegs-1 {
		return false
	}

	_, _, ok := pkgapk.ParseFileName(key[strings.LastIndexByte(key, '/')+1:])

	return ok
}

// apkFetchOpts returns the options for fetching key from an apk upstream. An
// index is fetched as is. A package needs the checksum its directory's index
// lists, read through the proxy cache before any download slot is taken (the
// index is a fetch of its own); the body is then parsed and compared with it
// once spooled. errNotFound is returned for a path that is no apk file or a
// package the index does not list.
func (s *Service) apkFetchOpts(ctx context.Context, repo *models.Repository, key string) (fetchOpts, error) {
	switch {
	case apkIndexKey(key):
		return fetchOpts{}, nil
	case !apkPackageKey(key):
		return fetchOpts{}, errNotFound
	}

	dir, file := key[:strings.LastIndexByte(key, '/')], key[strings.LastIndexByte(key, '/')+1:]

	sums, err := s.apkIndexChecksums(ctx, repo, dir+"/"+apkIndexName, file)
	if err != nil {
		return fetchOpts{}, err
	}

	if len(sums) == 0 {
		s.log.Debugw("apk package not listed in the upstream index", "repo", repo.Name, "path", key)
		return fetchOpts{}, errNotFound
	}

	return fetchOpts{verify: func(spool io.ReadSeeker) error { return verifyApkPackage(spool, sums) }}, nil
}

// apkIndexChecksums returns the Checksum of every entry of the index at
// indexKey whose file name is file.
func (s *Service) apkIndexChecksums(ctx context.Context, repo *models.Repository, indexKey, file string) ([]string, error) {
	res, err := s.resolve(ctx, repo, indexKey)
	if err != nil {
		return nil, err
	}

	if aerr := s.packuments.Acquire(ctx); aerr != nil {
		return nil, errUpstreamDown
	}
	defer s.packuments.Release()

	rc, err := s.blobStore.Get(ctx, res.entry.BlobKey)
	if err != nil {
		s.log.Errorw("failed to open cached apk index", "repo", repo.Name, "path", indexKey, "error", err)
		return nil, errInternal
	}
	defer func() { _ = rc.Close() }()

	pkgs, err := pkgapk.ParseIndex(rc)
	if err != nil {
		s.log.Warnw("proxy upstream apk index is invalid, refusing", "repo", repo.Name, "path", indexKey, "error", err)
		return nil, errVerify
	}

	var sums []string

	for i := range pkgs {
		if pkgs[i].FileName() == file {
			sums = append(sums, pkgs[i].Checksum)
		}
	}

	return sums, nil
}

var (
	errApkChecksum = errors.New("package does not match the checksum its upstream index lists")
	errApkSpool    = errors.New("spool is not random access")
)

// verifyApkPackage parses the spooled package and compares its checksum with
// the ones the index lists.
func verifyApkPackage(spool io.ReadSeeker, sums []string) error {
	ra, ok := spool.(io.ReaderAt)
	if !ok {
		return errApkSpool
	}

	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("size spool: %w", err)
	}

	pkg, err := pkgapk.ParsePackage(ra, size)
	if err != nil {
		return fmt.Errorf("parse package: %w", err)
	}

	if !slices.Contains(sums, pkg.Checksum) {
		return errApkChecksum
	}

	return nil
}

// APKHooks is the proxy hooks of the apk format.
var APKHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Fetch: (*Service).fetchAPK,
	// Packages whose checksum matched the upstream index; see apkFetchOpts.
	Immutable:  apkPackageKey,
	IsMetadata: apkIndexKey,

	Coordinates: apkCoordinates,
}

// fetchAPK is Hooks.Fetch of apk: a package is verified against the checksum
// of the upstream index.
func (s *Service) fetchAPK(ctx context.Context, repo *models.Repository, key, target string) (*models.ProxyCacheEntry, error) {
	o, err := s.apkFetchOpts(ctx, repo, key)
	if err != nil {
		return nil, err
	}

	return s.fetchURL(ctx, repo, key, target, o)
}
