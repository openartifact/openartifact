// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package itempath is the one reading of a release bundle item's artifact_path.
//
// The path is a client-supplied hint that names an artifact among the stored
// artifacts of the item's source repository that hold the item's content
// digest. Adding an item (internal/database BundleItemStored) and promoting it
// (internal/promote) both ask a Locator whether a stored artifact is the one
// the path names, so an item that was accepted is resolved to the same artifact
// when it is promoted.
//
// The clients send several spellings of the same artifact: the stored path, the
// stored path behind a leading slash, the listed file name (the web item picker
// and `oa bundle add --from-build`), the format's native repository URL path
// (`oa bundle add --path`), which for Ivy and sbt differs from the stored path
// when the group has several segments (`com/example/lib/1.0/lib-1.0.jar` is
// stored under `com.example/lib/1.0/lib-1.0.jar`), the URL with a `repos/<repo>/`
// prefix, a percent-encoded URL path, and for docker the digest, image, tag or
// distribution name.
//
// A Locator only ever compares spellings; the digest and the repository are
// selected by the caller's query, so no spelling can resolve to another
// repository's artifact or to an artifact stored as another digest.
package itempath

import (
	"net/url"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/artifactpath"
)

const (
	formatDocker = "docker"
	formatIvy    = "ivy"
	formatSBT    = "sbt"
)

// File is the identity of a stored package file a path can name.
type File struct {
	ID       string
	Path     string // package_files.path
	Filename string // package_files.filename
}

// Manifest is the identity of a stored docker manifest a path can name.
type Manifest struct {
	ID     string
	Image  string // "" for the repository's default image
	Digest string // with the algorithm, "sha256:..."
	SHA256 string // hex only
	Tags   []string
}

// Locator tells which stored artifacts an item's artifact_path names.
type Locator struct {
	repo string
	// any is set for an empty path: every artifact with the digest qualifies.
	any bool
	// exact are the spellings an artifact's path, file name or id must equal.
	exact []string
	// lasts are the last elements of the URL-like spellings, which the artifact's
	// file name may equal (a path under a directory the client knows but the
	// repository does not).
	lasts []string
}

// New reads the artifact_path of an item of format whose source repository is
// repo.
func New(format, repo, path string) Locator {
	l := Locator{repo: repo}

	p := trimSlashes(path)
	if p == "" {
		l.any = true

		return l
	}

	alts, foreign := spellings(format, repo, p)

	l.exact = alts

	if format == formatIvy || format == formatSBT {
		l.exact = append(l.exact, storedForms(alts)...)
	}

	if format != formatDocker && !foreign {
		for _, a := range alts {
			if i := strings.LastIndexByte(a, '/'); i >= 0 && i < len(a)-1 {
				l.lasts = append(l.lasts, a[i+1:])
			}
		}
	}

	return l
}

// File reports whether f is the stored file the path names.
func (l Locator) File(f File) bool {
	if l.any {
		return true
	}

	if l.equalsAny(trimSlashes(f.Path), trimSlashes(f.Filename), f.ID) {
		return true
	}

	return slices.Contains(l.lasts, f.Filename)
}

// Docker reports whether m is the stored manifest the path names.
func (l Locator) Docker(m Manifest) bool {
	if l.any {
		return true
	}

	return l.equalsAny(m.idents(l.repo)...)
}

// equalsAny reports whether one of the identifiers is a spelling of the path.
func (l Locator) equalsAny(idents ...string) bool {
	for _, id := range idents {
		if id == "" {
			continue
		}

		if slices.Contains(l.exact, id) {
			return true
		}
	}

	return false
}

// spellings returns the forms of the path p (leading slashes removed) that name
// an artifact of repo, p first. foreign is set when p is the URL of another
// repository (`repos/<other>/...`): only its literal spelling stays, so it can
// never resolve to an artifact by its trailing file name.
func spellings(format, repo, p string) ([]string, bool) {
	alts := []string{p}
	foreign := false

	if u, err := url.PathUnescape(p); err == nil && u != p {
		alts = append(alts, trimSlashes(u))
	}

	for _, a := range alts {
		if rest, ok := strings.CutPrefix(a, "repos/"); ok {
			name, tail, found := strings.Cut(rest, "/")

			switch {
			case !found || tail == "":
			case name == repo:
				alts = append(alts, trimSlashes(tail))
			default:
				foreign = true
			}
		}

		if rest, ok := strings.CutPrefix(a, "v2/"); ok && format == formatDocker {
			alts = append(alts, rest)
		}
	}

	return alts, foreign
}

// storedForms returns the stored paths of the Ivy or sbt files the native URL
// paths name: the group is the dotted form of every segment before the module
// for the Maven layout, whichever way a release before multi-segment groups
// stored it (see artifactpath.Split).
func storedForms(urlPaths []string) []string {
	var out []string

	for _, p := range urlPaths {
		cur, legacy, ok := artifactpath.Split(p)
		if !ok {
			continue
		}

		out = append(out, joinStored(cur), joinStored(legacy))
	}

	return out
}

func joinStored(p artifactpath.Path) string {
	return p.Group + "/" + p.Module + "/" + p.Version + "/" + p.Filename
}

// idents are the spellings of the manifest a path may use: row id, digest (with
// and without algorithm), image, distribution name, and each of those combined
// with the tags.
func (m Manifest) idents(repo string) []string {
	name := repo
	if m.Image != "" {
		name = repo + "/" + m.Image
	}

	ids := []string{m.ID, m.Digest, m.SHA256, name, name + "@" + m.Digest, name + "/manifests/" + m.Digest, m.Image}

	for _, t := range m.Tags {
		ids = append(ids, t, name+":"+t, name+"/manifests/"+t)

		if m.Image != "" {
			ids = append(ids, m.Image+":"+t)
		}
	}

	return ids
}

func trimSlashes(s string) string { return strings.TrimLeft(s, "/") }
