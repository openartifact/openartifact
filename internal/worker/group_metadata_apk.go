// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	"golang.org/x/sync/errgroup"
)

const (
	// maxUpstreamApkIndex caps the compressed size of an upstream APKINDEX.tar.gz
	// read for a merge (Alpine's largest is a few MiB).
	maxUpstreamApkIndex = 128 << 20
	// apkFetchWorkers bounds the concurrent reads of upstream indexes.
	apkFetchWorkers = 8
)

// validApkDir reports whether dir is a <branch>/<repo>/<arch> directory a
// group serves: its segments become proxy request paths and database keys.
func validApkDir(dir string) bool {
	parts := strings.Split(dir, "/")
	if len(parts) != 3 {
		return false
	}

	for _, p := range parts {
		if !pkgapk.ValidSegment(p) {
			return false
		}
	}

	return true
}

// apkRowDir is the <branch>/<repo>/<arch> directory a stored package lives in.
func apkRowDir(row *models.ApkPackage) string {
	return row.Branch + "/" + row.Repo + "/" + row.Arch
}

// apkDirSource is what one member offers for one directory.
type apkDirSource struct {
	member  *models.Repository
	proxy   bool
	entries []pkgapk.Package
}

// apkSelected is an index entry of the merged index and the member that serves
// its file.
type apkSelected struct {
	pkg    pkgapk.Package
	member *models.Repository
}

// mergeApkDir merges the members' entries of one directory, sources in group
// priority order:
//
//   - an entry of a proxy source is dropped when its package name is in hidden
//     (the names the group's hosted members hold: hosted wins; nil for a group
//     that merges proxy versions);
//   - of entries with the same file name (<name>-<version>.apk) the first
//     source's wins, so a higher-priority member shadows the same release of a
//     lower one.
//
// A noarch package is an ordinary entry of the directory it is listed in: each
// directory is merged on its own, so it appears exactly in the directories its
// members list it in. The result keeps source order.
func mergeApkDir(sources []apkDirSource, hidden map[string]struct{}) []apkSelected {
	seen := make(map[string]struct{})

	var out []apkSelected

	for _, src := range sources {
		for i := range src.entries {
			p := src.entries[i]

			if src.proxy {
				if _, isHidden := hidden[p.Name]; isHidden {
					continue
				}
			}

			file := p.FileName()
			if _, dup := seen[file]; dup {
				continue
			}

			seen[file] = struct{}{}
			out = append(out, apkSelected{pkg: p, member: src.member})
		}
	}

	return out
}

// fetchApkIndex reads and parses a proxy member's index of dir. found is false
// when the upstream has no such directory.
func (w *GroupMetadataWorker) fetchApkIndex(ctx context.Context, m *models.Repository, dir string) ([]pkgapk.Package, bool, error) {
	rc, err := w.opener.Open(ctx, m, dir+"/"+formatapk.IndexName)
	if errors.Is(err, proxy.ErrNotFound) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err //nolint:wrapcheck // callers classify the sentinel
	}

	defer func() { _ = rc.Close() }()

	pkgs, err := pkgapk.ParseIndex(&limitedReader{r: rc, left: maxUpstreamApkIndex})
	if err != nil {
		return nil, false, fmt.Errorf("group worker: reading index %s of %s: %w", dir, m.Name, err)
	}

	return pkgs, true, nil
}

// limitedReader is io.LimitedReader that fails, rather than reports EOF, once
// the limit is exceeded: a truncated index must not be taken for a whole one.
type limitedReader struct {
	r    io.Reader
	left int64
}

var errApkIndexTooLarge = errors.New("upstream apk index exceeds its size limit")

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, errApkIndexTooLarge
	}

	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}

	n, err := l.r.Read(p)

	l.left -= int64(n)
	if l.left < 0 {
		return n, errApkIndexTooLarge
	}

	return n, err //nolint:wrapcheck // passes the underlying reader's error through
}

// checkApkDir returns group.ErrDistNotFound unless some member has dir (a
// proxy member is asked, its answer cached by the proxy).
func (w *GroupMetadataWorker) checkApkDir(ctx context.Context, dir string, members []*models.Repository) error {
	if !validApkDir(dir) {
		return group.ErrDistNotFound
	}

	var firstErr error

	for _, m := range members {
		if m.RepoType == repotype.Proxy {
			_, found, err := w.fetchApkIndex(ctx, m, dir)
			if err != nil {
				firstErr = cmpFirst(firstErr, err)
				continue
			}

			if found {
				return nil
			}

			continue
		}

		rows, err := w.db.ListApkPackages(ctx, m.ID)
		if err != nil {
			return fmt.Errorf("group worker: listing packages of %s: %w", m.Name, err)
		}

		for _, row := range rows {
			if apkRowDir(row) == dir {
				return nil
			}
		}
	}

	if firstErr != nil {
		return firstErr
	}

	return group.ErrDistNotFound
}

// loadHostedApk reads the hosted members' packages by member and directory and
// seeds the hosted-wins set with their names.
func (j *groupJob) loadHostedApk(ctx context.Context) (map[string]map[string][]pkgapk.Package, error) {
	out := make(map[string]map[string][]pkgapk.Package)

	for _, m := range j.members {
		if m.RepoType == repotype.Proxy {
			continue
		}

		rows, err := j.w.db.ListApkPackages(ctx, m.ID)
		if err != nil {
			return nil, fmt.Errorf("group worker: listing packages of %s: %w", m.Name, err)
		}

		byDir := make(map[string][]pkgapk.Package)

		for _, row := range rows {
			dir := apkRowDir(row)
			byDir[dir] = append(byDir[dir], ApkRowPackage(row))

			if j.hostedNames != nil {
				j.hostedNames[row.Name] = struct{}{}
			}
		}

		out[m.ID] = byDir
	}

	return out, nil
}

// apkFetch is the read of one proxy member's index of one directory.
type apkFetch struct {
	member  *models.Repository
	dir     string
	entries []pkgapk.Package
	found   bool
	err     error
}

// fetchProxyIndexes reads every proxy member's index of every directory,
// concurrently, in member-major order. A member that cannot be read leaves the
// generation partial.
func (j *groupJob) fetchProxyIndexes(ctx context.Context, dirs []string) ([]*apkFetch, error) {
	var fetches []*apkFetch

	for _, m := range j.members {
		if m.RepoType != repotype.Proxy {
			continue
		}

		for _, dir := range dirs {
			fetches = append(fetches, &apkFetch{member: m, dir: dir})
		}
	}

	var g errgroup.Group

	g.SetLimit(apkFetchWorkers)

	for _, f := range fetches {
		g.Go(func() error {
			f.entries, f.found, f.err = j.w.fetchApkIndex(ctx, f.member, f.dir)
			return nil
		})
	}

	_ = g.Wait()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("group worker: reading proxy indexes: %w", err)
	}

	for _, f := range fetches {
		if f.err != nil {
			j.noteFetchError(f.member, f.dir+"/"+formatapk.IndexName, f.err)
		}
	}

	return fetches, nil
}

// buildApk renders the merged index of every directory the members have: the
// hosted members' directories and the extra ones (named by an earlier request)
// that a proxy member has. A directory a proxy member could not be read for,
// and nothing else offers, is not rendered and keeps its previous index until
// the retry.
func (j *groupJob) buildApk(ctx context.Context, extra []string) error {
	j.hostedNames = make(map[string]struct{})

	hosted, err := j.loadHostedApk(ctx)
	if err != nil {
		return err
	}

	if j.group.MergeProxyVersions {
		j.hostedNames = nil
	}

	dirSet := make(map[string]struct{})

	for _, byDir := range hosted {
		for dir := range byDir {
			dirSet[dir] = struct{}{}
		}
	}

	for _, dir := range extra {
		if validApkDir(dir) {
			dirSet[dir] = struct{}{}
		}
	}

	dirs := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		dirs = append(dirs, dir)
	}

	sort.Strings(dirs)

	fetches, err := j.fetchProxyIndexes(ctx, dirs)
	if err != nil {
		return err
	}

	byDir := make(map[string][]pkgapk.Package, len(dirs))

	for _, dir := range dirs {
		sources, unreadable := j.apkSources(dir, hosted, fetches)

		if len(sources) == 0 {
			if unreadable {
				// Not known to be gone: retried, its previous index stays.
				j.dists = append(j.dists, dir)
				j.keep[dir+"/"+formatapk.IndexName] = struct{}{}
			}

			continue
		}

		j.dists = append(j.dists, dir)
		byDir[dir+"/"+formatapk.IndexName] = j.claimApk(dir, mergeApkDir(sources, j.hostedNames))
	}

	blobs, err := renderApkIndexes(j.group.Name, byDir, j.apkSigner)
	if err != nil {
		return err
	}

	maps.Copy(j.blobs, blobs)

	return nil
}

// apkSources collects, in member order, what the members offer for dir.
// unreadable reports that a proxy member failed to answer for it.
func (j *groupJob) apkSources(dir string, hosted map[string]map[string][]pkgapk.Package, fetches []*apkFetch) ([]apkDirSource, bool) {
	var (
		sources    []apkDirSource
		unreadable bool
	)

	for _, m := range j.members {
		if m.RepoType != repotype.Proxy {
			if entries, ok := hosted[m.ID][dir]; ok {
				sources = append(sources, apkDirSource{member: m, entries: entries})
			}

			continue
		}

		for _, f := range fetches {
			if f.member.ID != m.ID || f.dir != dir {
				continue
			}

			switch {
			case f.err != nil:
				unreadable = true
			case f.found:
				sources = append(sources, apkDirSource{member: m, proxy: true, entries: f.entries})
			}
		}
	}

	return sources, unreadable
}

// claimApk registers the owner of the file of every selected entry and returns
// the entries to list.
func (j *groupJob) claimApk(dir string, selected []apkSelected) []pkgapk.Package {
	pkgs := make([]pkgapk.Package, 0, len(selected))

	for _, s := range selected {
		if !j.claim(dir+"/"+s.pkg.FileName(), s.member.ID, s.pkg.Checksum) {
			continue
		}

		pkgs = append(pkgs, s.pkg)
	}

	return pkgs
}

// keptPrefixes are the owner path prefixes of the directories left as they
// were: their package files stay served by the owners of the previous index.
func (j *groupJob) keptPrefixes() []string {
	prefixes := make([]string, 0, len(j.keep))
	for rel := range j.keep {
		prefixes = append(prefixes, j.scope+strings.TrimSuffix(rel, formatapk.IndexName))
	}

	return prefixes
}

// addEmptyApkIndexes adds an empty index to the generated files for every
// directory the stored metadata has an index for but no member offers any more
// (a client that knew the directory keeps getting a valid, empty index instead
// of a 404), except those kept for a retry.
func (j *groupJob) addEmptyApkIndexes(oldSHA map[string]string) error {
	for path := range oldSHA {
		rel := strings.TrimPrefix(path, j.scope)
		if _, ok := j.blobs[rel]; ok || !isApkIndexPath(rel) {
			continue
		}

		if _, kept := j.keep[rel]; kept {
			continue
		}

		index, err := pkgapk.BuildIndex(nil, "OpenArtifact apk repository "+j.group.Name, j.apkSigner)
		if err != nil {
			return fmt.Errorf("group worker: building empty index %s: %w", rel, err)
		}

		j.blobs[rel] = index
	}

	return nil
}
