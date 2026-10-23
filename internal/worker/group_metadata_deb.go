// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"  //nolint:gosec // required by the protocol
	"crypto/sha1" //nolint:gosec // required by the protocol
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/xzutil"
)

const (
	// maxUpstreamRelease caps an upstream Release file read for a merge.
	maxUpstreamRelease = 64 << 20
	// debStanzaHint is a typical Packages stanza size, used to size the xz
	// dictionary of an index.
	debStanzaHint = 1024
	// debProxyIndexHint is the expected size of a proxy member's index (a
	// full Debian suite component is tens of MiB).
	debProxyIndexHint = 32 << 20
)

// validDistName restricts the distribution names a group serves: they become
// proxy request paths and database keys.
var validDistName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// ValidDistName reports whether name is acceptable as a Debian distribution of
// a group.
func ValidDistName(name string) bool { return validDistName.MatchString(name) }

// debHostedDist is one distribution of a hosted deb member.
type debHostedDist struct {
	components    []string
	architectures []string
	entries       []models.DebPackageEntry
}

// debContribution is what one member offers for a distribution.
type debContribution struct {
	member *models.Repository
	hosted *debHostedDist             // set for hosted members
	rel    *formatdeb.UpstreamRelease // set for proxy members
}

// checkDist returns group.ErrDistNotFound unless some member has dist (a
// proxy member is asked, its answer cached by the proxy).
func (w *GroupMetadataWorker) checkDist(ctx context.Context, grp *models.Repository, dist string, members []*models.Repository) error {
	if grp.Format != "deb" {
		return nil
	}

	if !ValidDistName(dist) {
		return group.ErrDistNotFound
	}

	var firstErr error

	for _, m := range members {
		if m.RepoType == repotype.Proxy {
			rel, err := w.fetchRelease(ctx, m, dist)
			if err != nil {
				firstErr = cmpFirst(firstErr, err)
				continue
			}

			if rel != nil {
				return nil
			}

			continue
		}

		dists, err := w.db.GetDebianDistributionsByRepo(ctx, m.ID)
		if err != nil {
			return fmt.Errorf("group worker: querying distributions of %s: %w", m.Name, err)
		}

		for _, d := range dists {
			if d.Name == dist {
				return nil
			}
		}
	}

	if firstErr != nil {
		return firstErr
	}

	return group.ErrDistNotFound
}

func cmpFirst(first, err error) error {
	if first != nil {
		return first
	}

	return err
}

// fetchRelease reads the Release (else InRelease) of dist from a proxy member.
// A nil Release with a nil error means the upstream does not have the
// distribution.
func (w *GroupMetadataWorker) fetchRelease(ctx context.Context, m *models.Repository, dist string) (*formatdeb.UpstreamRelease, error) {
	for _, name := range []string{"Release", "InRelease"} {
		data, err := w.readAll(ctx, m, "dists/"+dist+"/"+name, maxUpstreamRelease)
		if errors.Is(err, proxy.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		rel := formatdeb.ParseRelease(data)

		return &rel, nil
	}

	return nil, nil //nolint:nilnil // absent is not an error
}

// readAll reads a proxy member's file in full, at most limit bytes.
func (w *GroupMetadataWorker) readAll(ctx context.Context, m *models.Repository, relPath string, limit int64) ([]byte, error) {
	rc, err := w.opener.Open(ctx, m, relPath)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers classify the sentinel
	}

	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("group worker: reading %s of %s: %w", relPath, m.Name, err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("group worker: %s of %s exceeds %d bytes", relPath, m.Name, limit) //nolint:err113 // dynamic context
	}

	return data, nil
}

// loadHostedDeb reads the distributions of the hosted members: for each its
// components, architectures and packages. Hosted members' package names also
// seed the hosted-wins set.
func (j *groupJob) loadHostedDeb(ctx context.Context) (map[string]map[string]*debHostedDist, error) {
	db := j.w.db
	out := make(map[string]map[string]*debHostedDist)

	for _, m := range j.members {
		if m.RepoType == repotype.Proxy {
			continue
		}

		dists, err := db.GetDebianDistributionsByRepo(ctx, m.ID)
		if err != nil {
			return nil, fmt.Errorf("group worker: querying distributions of %s: %w", m.Name, err)
		}

		entries, err := db.ListDebPackageEntries(ctx, m.ID, "", "", "")
		if err != nil {
			return nil, fmt.Errorf("group worker: querying packages of %s: %w", m.Name, err)
		}

		byDist := make(map[string]*debHostedDist, len(dists))

		for _, d := range dists {
			hd := &debHostedDist{}

			comps, err := db.GetDebianComponentsByDist(ctx, d.ID)
			if err != nil {
				return nil, fmt.Errorf("group worker: querying components of %s/%s: %w", m.Name, d.Name, err)
			}

			for _, c := range comps {
				hd.components = append(hd.components, c.Name)
			}

			archs, err := db.GetDebianArchitecturesByDist(ctx, d.ID)
			if err != nil {
				return nil, fmt.Errorf("group worker: querying architectures of %s/%s: %w", m.Name, d.Name, err)
			}

			for _, a := range archs {
				hd.architectures = append(hd.architectures, a.Name)
			}

			byDist[d.Name] = hd
		}

		for _, e := range entries {
			if hd := byDist[e.Distribution]; hd != nil {
				hd.entries = append(hd.entries, e)
			}

			j.hostedNames[e.Name] = struct{}{}
		}

		out[m.ID] = byDist
	}

	return out, nil
}

// buildDeb generates every distribution of the group: the hosted members' ones
// and the extra ones (named by an earlier request) that some member has.
func (j *groupJob) buildDeb(ctx context.Context, extra []string) error {
	j.hostedNames = make(map[string]struct{})

	hosted, err := j.loadHostedDeb(ctx)
	if err != nil {
		return err
	}

	if j.group.MergeProxyVersions {
		j.hostedNames = nil
	}

	names := make(map[string]struct{})

	for _, byDist := range hosted {
		for name := range byDist {
			names[name] = struct{}{}
		}
	}

	for _, name := range extra {
		if ValidDistName(name) {
			names[name] = struct{}{}
		}
	}

	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}

	sort.Strings(ordered)

	for _, dist := range ordered {
		found, err := j.buildDebDist(ctx, dist, hosted)
		if err != nil {
			return err
		}

		if found {
			j.dists = append(j.dists, dist)
		}
	}

	j.addPublicKey()

	return nil
}

// addPublicKey publishes the signing key clients import to verify the group.
func (j *groupJob) addPublicKey() {
	if j.signer == nil {
		return
	}

	key, err := j.signer.PublicKey()
	if err != nil {
		j.w.log.Warnw("group worker: failed to export public key", "group", j.group.Name, "error", err)
		return
	}

	j.blobs["public.key"] = key
}

// buildDebDist renders the Release files and Packages indexes of one
// distribution. found is false when no member has the distribution.
func (j *groupJob) buildDebDist(ctx context.Context, dist string, hosted map[string]map[string]*debHostedDist) (bool, error) {
	var contribs []debContribution

	for _, m := range j.members {
		if m.RepoType == repotype.Proxy {
			rel, err := j.w.fetchRelease(ctx, m, dist)
			if err != nil {
				j.noteFetchError(m, "dists/"+dist+"/Release", err)
				continue
			}

			if rel != nil {
				contribs = append(contribs, debContribution{member: m, rel: rel})
			}

			continue
		}

		if hd := hosted[m.ID][dist]; hd != nil {
			contribs = append(contribs, debContribution{member: m, hosted: hd})
		}
	}

	if len(contribs) == 0 {
		return false, nil
	}

	comps, archs := debComponentsAndArchs(contribs)

	releaseGen := formatdeb.NewReleaseFileGenerator("openartifact", "openartifact", dist, dist)
	for _, c := range comps {
		releaseGen.AddComponent(c)
	}

	for _, a := range archs {
		releaseGen.AddArchitecture(a)
	}

	var files []formatdeb.FileInfo

	for _, comp := range comps {
		for _, arch := range archs {
			idx, err := j.buildDebIndex(ctx, dist, comp, arch, contribs)
			if err != nil {
				return false, err
			}

			base := fmt.Sprintf("%s/binary-%s/Packages", comp, arch)

			files = append(files, idx.plainInfo(base))

			for ext, data := range map[string][]byte{".gz": idx.gz, ".xz": idx.xz} {
				j.blobs["dists/"+dist+"/"+base+ext] = data
				md5sum, sha1sum, sha256sum := formatdeb.CalculateChecksums(data)
				files = append(files, formatdeb.FileInfo{Filename: base + ext, Size: int64(len(data)), MD5sum: md5sum, SHA1: sha1sum, SHA256: sha256sum})
			}
		}
	}

	sort.Slice(files, func(a, b int) bool { return files[a].Filename < files[b].Filename })

	release := []byte(releaseGen.GenerateRelease(files))
	j.blobs["dists/"+dist+"/Release"] = release

	if j.signer != nil {
		signed, err := formatdeb.NewReleaseSigner(j.signer).SignRelease(release)
		if err != nil {
			j.w.log.Warnw("group worker: failed to sign release", "group", j.group.Name, "dist", dist, "error", err)
		} else {
			j.blobs["dists/"+dist+"/InRelease"] = signed.InRelease
			j.blobs["dists/"+dist+"/Release.gpg"] = signed.ReleaseGPG
		}
	}

	return true, nil
}

// debComponentsAndArchs is the union of the members' components and
// architectures. Architecture "all" gets an index of its own only when no
// member has a real one (its packages are listed in every real index).
func debComponentsAndArchs(contribs []debContribution) ([]string, []string) {
	var comps, archs []string

	hasAll := false

	add := func(list *[]string, v string) {
		if v != "" && !slices.Contains(*list, v) {
			*list = append(*list, v)
		}
	}

	for _, c := range contribs {
		var cs, as []string

		if c.hosted != nil {
			cs, as = c.hosted.components, c.hosted.architectures
		} else {
			cs, as = c.rel.Components, c.rel.Architectures
		}

		for _, v := range cs {
			add(&comps, v)
		}

		for _, v := range as {
			if v == "all" {
				hasAll = true
				continue
			}

			add(&archs, v)
		}
	}

	if len(archs) == 0 && hasAll {
		archs = []string{"all"}
	}

	sort.Strings(comps)
	sort.Strings(archs)

	return comps, archs
}

// debIndex collects a merged Packages index, compressed as it is written.
type debIndex struct {
	gzBuf, xzBuf bytes.Buffer
	gz, xz       []byte
	gw           *gzip.Writer
	xw           io.WriteCloser
	out          io.Writer

	// plain hashes the uncompressed index. Release lists it although it is
	// not stored: apt skips an index whose uncompressed entry is missing.
	plainMD5, plainSHA1, plainSHA256 hash.Hash
	plainSize                        int64
}

func newDebIndex(sizeHint int) (*debIndex, error) {
	idx := &debIndex{}
	idx.gw = gzip.NewWriter(&idx.gzBuf)

	xw, err := xzutil.NewWriter(&idx.xzBuf, sizeHint)
	if err != nil {
		return nil, fmt.Errorf("group worker: xz compression: %w", err)
	}

	idx.xw = xw
	idx.plainMD5, idx.plainSHA1, idx.plainSHA256 = md5.New(), sha1.New(), sha256.New() //nolint:gosec // required by the protocol
	idx.out = io.MultiWriter(idx.gw, idx.xw, idx.plainMD5, idx.plainSHA1, idx.plainSHA256)

	return idx, nil
}

// plainInfo describes the uncompressed index for the Release file.
func (i *debIndex) plainInfo(name string) formatdeb.FileInfo {
	return formatdeb.FileInfo{
		Filename: name,
		Size:     i.plainSize,
		MD5sum:   hex.EncodeToString(i.plainMD5.Sum(nil)),
		SHA1:     hex.EncodeToString(i.plainSHA1.Sum(nil)),
		SHA256:   hex.EncodeToString(i.plainSHA256.Sum(nil)),
	}
}

func (i *debIndex) close() error {
	if err := i.gw.Close(); err != nil {
		return fmt.Errorf("group worker: gzip compression: %w", err)
	}

	if err := i.xw.Close(); err != nil {
		return fmt.Errorf("group worker: xz compression: %w", err)
	}

	i.gz, i.xz = i.gzBuf.Bytes(), i.xzBuf.Bytes()

	return nil
}

// buildDebIndex merges the members' packages of dist/comp/binary-arch in
// member order: the first member to list a package version wins.
func (j *groupJob) buildDebIndex(ctx context.Context, dist, comp, arch string, contribs []debContribution) (*debIndex, error) {
	hint, proxies := 0, false

	for _, c := range contribs {
		hint += len(c.hostedEntries()) * debStanzaHint
		proxies = proxies || c.rel != nil
	}

	if proxies {
		hint += debProxyIndexHint
	}

	idx, err := newDebIndex(hint)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})

	add := func(m *models.Repository, st formatdeb.Stanza, hosted bool) error {
		return j.addDebStanza(idx, seen, m, st, hosted)
	}

	for _, c := range contribs {
		if c.hosted != nil {
			for _, e := range c.hosted.entries {
				if e.ComponentName != comp || (e.Arch != arch && e.Arch != "all") {
					continue
				}

				st := formatdeb.Stanza{
					Package: e.Name, Version: e.Version, Architecture: e.Arch,
					Filename: formatdeb.GetPoolPath(e.ComponentName, e.Name, e.Version, e.Filename),
					SHA256:   e.SHA256, Text: formatdeb.FormatPackagesEntry(e),
				}

				if err := add(c.member, st, true); err != nil {
					return nil, err
				}
			}

			continue
		}

		if !c.rel.HasIndex(comp, arch) {
			continue
		}

		if err := j.scanProxyIndex(ctx, c, dist, comp, arch, func(st formatdeb.Stanza) error { return add(c.member, st, false) }); err != nil {
			return nil, err
		}
	}

	if err := idx.close(); err != nil {
		return nil, err
	}

	return idx, nil
}

func (c debContribution) hostedEntries() []models.DebPackageEntry {
	if c.hosted == nil {
		return nil
	}

	return c.hosted.entries
}

// addDebStanza writes st to the index unless an earlier member provides the
// same package version, hosted-wins hides it, or its file cannot be served
// unambiguously.
func (j *groupJob) addDebStanza(idx *debIndex, seen map[string]struct{}, m *models.Repository, st formatdeb.Stanza, hosted bool) error {
	key := st.Package + "|" + st.Version + "|" + st.Architecture
	if _, dup := seen[key]; dup {
		return nil
	}

	if !hosted && j.hostedNames != nil {
		if _, hidden := j.hostedNames[st.Package]; hidden {
			return nil
		}
	}

	file := path.Clean(st.Filename)
	if st.Filename == "" || file != st.Filename || !strings.HasPrefix(file, "pool/") || strings.Contains(file, "..") {
		return nil
	}

	if !j.claim(file, m.ID, st.SHA256) {
		return nil
	}

	seen[key] = struct{}{}

	idx.plainSize += int64(len(st.Text))

	if _, err := io.WriteString(idx.out, st.Text); err != nil {
		return fmt.Errorf("group worker: writing Packages: %w", err)
	}

	return nil
}

// scanProxyIndex streams the entries of a proxy member's Packages index,
// trying the compressions its Release lists best-first. An index the upstream
// lacks contributes nothing; any other failure makes the generation partial.
func (j *groupJob) scanProxyIndex(ctx context.Context, c debContribution, dist, comp, arch string, fn func(formatdeb.Stanza) error) error {
	for _, variant := range c.rel.PackagesVariants(comp, arch) {
		rel := "dists/" + dist + "/" + variant

		rc, err := j.w.opener.Open(ctx, c.member, rel)
		if errors.Is(err, proxy.ErrNotFound) {
			continue
		}

		if err != nil {
			j.noteFetchError(c.member, rel, err)
			return nil
		}

		dec, err := formatdeb.DecompressIndex(rc, variant)
		if err != nil {
			j.noteFetchError(c.member, rel, err)
			return nil
		}

		// An error of fn (writing the merged index) aborts the generation; a
		// malformed or truncated upstream index keeps the entries read so
		// far and makes the generation partial, redone soon.
		var fnErr error

		err = formatdeb.ScanPackages(dec, func(st formatdeb.Stanza) error {
			fnErr = fn(st)
			return fnErr
		})
		_ = dec.Close()

		if fnErr != nil {
			return fnErr
		}

		if err != nil {
			j.noteFetchError(c.member, rel, err)
		}

		return nil
	}

	return nil
}
