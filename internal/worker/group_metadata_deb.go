// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/md5"  //nolint:gosec // required by the protocol
	"crypto/sha1" //nolint:gosec // required by the protocol
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/xzutil"
	"golang.org/x/sync/errgroup"
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
	// debFetchWorkers bounds the concurrent reads of upstream metadata (each
	// holds a decompressor and, on a cold proxy cache, an upstream request).
	debFetchWorkers = 8
	// debCompressWorkers bounds the indexes compressed at once; each holds an
	// xz encoder (up to about 50 MB) and its output.
	debCompressWorkers = 3
	// maxBufferHint caps the output buffer pre-sized from the index size.
	maxBufferHint = 64 << 20
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
// proxy member is asked, its answer cached by the proxy). For an apk group dist
// is a branch/repo/arch directory.
func (w *GroupMetadataWorker) checkDist(ctx context.Context, grp *models.Repository, dist string, members []*models.Repository) error {
	if grp.Format == "apk" {
		return w.checkApkDir(ctx, dist, members)
	}

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
	contribs := j.collectContributions(ctx, dist, hosted)
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

	indexes, err := j.buildDebIndexes(ctx, dist, comps, archs, contribs)
	if err != nil {
		return false, err
	}

	var files []formatdeb.FileInfo

	for _, idx := range indexes {
		files = append(files, idx.plainInfo(idx.base))

		for ext, data := range map[string][]byte{".gz": idx.gz, ".xz": idx.xz} {
			j.blobs["dists/"+dist+"/"+idx.base+ext] = data
			md5sum, sha1sum, sha256sum := formatdeb.CalculateChecksums(data)
			files = append(
				files,
				formatdeb.FileInfo{Filename: idx.base + ext, Size: int64(len(data)), MD5sum: md5sum, SHA1: sha1sum, SHA256: sha256sum},
			)
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

// collectContributions asks every member what it offers for dist: the proxy
// members' Release files are fetched concurrently (each is a round trip to its
// upstream on a cold cache), the result keeps member order.
func (j *groupJob) collectContributions(ctx context.Context, dist string, hosted map[string]map[string]*debHostedDist) []debContribution {
	slots := make([]debContribution, len(j.members))
	errs := make([]error, len(j.members))

	var wg errgroup.Group

	wg.SetLimit(debFetchWorkers)

	for i, m := range j.members {
		if m.RepoType != repotype.Proxy {
			if hd := hosted[m.ID][dist]; hd != nil {
				slots[i] = debContribution{member: m, hosted: hd}
			}

			continue
		}

		wg.Go(func() error {
			rel, err := j.w.fetchRelease(ctx, m, dist)
			if err != nil {
				errs[i] = err
			} else if rel != nil {
				slots[i] = debContribution{member: m, rel: rel}
			}

			return nil
		})
	}

	_ = wg.Wait() // the tasks record their errors in errs

	contribs := make([]debContribution, 0, len(slots))

	for i, c := range slots {
		if errs[i] != nil {
			j.noteFetchError(j.members[i], "dists/"+dist+"/Release", errs[i])
		}

		if c.member != nil {
			contribs = append(contribs, c)
		}
	}

	return contribs
}

// debIndex is a merged Packages index: the accepted stanzas, then their
// compressed forms and checksums.
type debIndex struct {
	// base is the path of the uncompressed index relative to the distribution.
	base string
	// texts are the accepted stanzas in output order; dropped by compress.
	texts []string
	// size is the uncompressed size, the sum of the texts' lengths.
	size int64
	// dictHint sizes the xz dictionary.
	dictHint int

	gz, xz []byte

	// plain describes the uncompressed index for the Release file. Release lists
	// it although it is not stored: apt skips an index whose uncompressed entry
	// is missing.
	plainMD5, plainSHA1, plainSHA256 string
}

// plainInfo describes the uncompressed index for the Release file.
func (i *debIndex) plainInfo(name string) formatdeb.FileInfo {
	return formatdeb.FileInfo{
		Filename: name,
		Size:     i.size,
		MD5sum:   i.plainMD5,
		SHA1:     i.plainSHA1,
		SHA256:   i.plainSHA256,
	}
}

// compress produces the gzip and xz forms and the checksums of the plain index.
// The three are independent passes over the texts, so they run concurrently
// (xz dominates; it is an order of magnitude slower than gzip).
func (i *debIndex) compress() error {
	var (
		gzBuf, xzBuf bytes.Buffer
		wg           errgroup.Group
	)

	gzBuf.Grow(int(min(i.size/4, maxBufferHint)))
	xzBuf.Grow(int(min(i.size/6, maxBufferHint)))

	wg.Go(func() error {
		gw := gzip.NewWriter(&gzBuf)

		if err := writeAll(gw, i.texts); err != nil {
			return err
		}

		if err := gw.Close(); err != nil {
			return fmt.Errorf("group worker: gzip compression: %w", err)
		}

		return nil
	})

	wg.Go(func() error {
		xw, err := xzutil.NewWriter(&xzBuf, i.dictHint)
		if err != nil {
			return fmt.Errorf("group worker: xz compression: %w", err)
		}

		if err := writeAll(xw, i.texts); err != nil {
			return err
		}

		if err := xw.Close(); err != nil {
			return fmt.Errorf("group worker: xz compression: %w", err)
		}

		return nil
	})

	wg.Go(func() error {
		md5h, sha1h, sha256h := md5.New(), sha1.New(), sha256.New() //nolint:gosec // required by the protocol

		if err := writeAll(io.MultiWriter(md5h, sha1h, sha256h), i.texts); err != nil {
			return err
		}

		i.plainMD5 = hex.EncodeToString(md5h.Sum(nil))
		i.plainSHA1 = hex.EncodeToString(sha1h.Sum(nil))
		i.plainSHA256 = hex.EncodeToString(sha256h.Sum(nil))

		return nil
	})

	if err := wg.Wait(); err != nil {
		return err //nolint:wrapcheck // the tasks wrap
	}

	i.gz, i.xz = gzBuf.Bytes(), xzBuf.Bytes()
	i.texts = nil

	return nil
}

func writeAll(w io.Writer, texts []string) error {
	for _, t := range texts {
		if _, err := io.WriteString(w, t); err != nil {
			return fmt.Errorf("group worker: writing Packages: %w", err)
		}
	}

	return nil
}

// debProxyScan is what one proxy member's Packages index yielded: every stanza
// read, and the failures that make the generation partial.
type debProxyScan struct {
	stanzas []formatdeb.Stanza
	errs    []debFetchError
}

type debFetchError struct {
	path string
	err  error
}

// buildDebIndexes merges the members' packages of every dist/comp/binary-arch
// in member order (the first member to list a package version wins) and
// compresses the results.
//
// The work is split by cost. The proxy members' indexes are fetched and parsed
// concurrently (decompressing an upstream index dominates a cold request). The
// merge itself is a cheap pass that runs sequentially in index and member
// order, because which member owns a file name and which duplicate is dropped
// must not depend on scheduling. The compression of the merged indexes runs
// concurrently again.
func (j *groupJob) buildDebIndexes(
	ctx context.Context,
	dist string,
	comps, archs []string,
	contribs []debContribution,
) ([]*debIndex, error) {
	indexes := make([]*debIndex, 0, len(comps)*len(archs))
	for _, comp := range comps {
		for _, arch := range archs {
			indexes = append(indexes, &debIndex{base: fmt.Sprintf("%s/binary-%s/Packages", comp, arch), dictHint: j.debDictHint(contribs)})
		}
	}

	scans := j.scanDebProxies(ctx, dist, comps, archs, contribs)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("group worker: building %s: %w", dist, err)
	}

	for n, idx := range indexes {
		comp, arch := comps[n/len(archs)], archs[n%len(archs)]
		j.mergeDebIndex(idx, comp, arch, contribs, scans[n])
		scans[n] = nil // let the parsed stanzas go once merged
	}

	if err := compressDebIndexes(ctx, indexes); err != nil {
		return nil, err
	}

	return indexes, nil
}

// debDictHint sizes the xz dictionary: a proxy member's index is tens of MiB,
// a hosted member's is a stanza per entry.
func (j *groupJob) debDictHint(contribs []debContribution) int {
	hint, proxies := 0, false

	for _, c := range contribs {
		hint += len(c.hostedEntries()) * debStanzaHint
		proxies = proxies || c.rel != nil
	}

	if proxies {
		hint += debProxyIndexHint
	}

	return hint
}

// scanDebProxies reads, concurrently, the Packages index every proxy member
// offers for each comp/arch. The result is indexed like the indexes, then like
// contribs; nil where the member has no such index.
func (j *groupJob) scanDebProxies(ctx context.Context, dist string, comps, archs []string, contribs []debContribution) [][]*debProxyScan {
	scans := make([][]*debProxyScan, len(comps)*len(archs))

	var wg errgroup.Group

	wg.SetLimit(debFetchWorkers)

	for n := range scans {
		scans[n] = make([]*debProxyScan, len(contribs))
		comp, arch := comps[n/len(archs)], archs[n%len(archs)]

		for ci, c := range contribs {
			if c.rel == nil || !c.rel.HasIndex(comp, arch) {
				continue
			}

			scan := &debProxyScan{}
			scans[n][ci] = scan

			wg.Go(func() error {
				j.scanProxyIndex(ctx, c, dist, comp, arch, scan)
				return nil
			})
		}
	}

	_ = wg.Wait() // the tasks record their failures in the scans

	return scans
}

// mergeDebIndex fills idx with the accepted stanzas of comp/arch: the
// members' in order, hosted ones from their entries and proxy ones from scans.
func (j *groupJob) mergeDebIndex(idx *debIndex, comp, arch string, contribs []debContribution, scans []*debProxyScan) {
	seen := make(map[debStanzaKey]struct{})

	add := func(m *models.Repository, st formatdeb.Stanza, hosted bool) {
		if text, ok := j.acceptDebStanza(seen, m, st, hosted); ok {
			idx.texts = append(idx.texts, text)
			idx.size += int64(len(text))
		}
	}

	for ci, c := range contribs {
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

				add(c.member, st, true)
			}

			continue
		}

		scan := scans[ci]
		if scan == nil {
			continue
		}

		for _, fe := range scan.errs {
			j.noteFetchError(c.member, fe.path, fe.err)
		}

		for _, st := range scan.stanzas {
			add(c.member, st, false)
		}
	}
}

// compressDebIndexes compresses the indexes concurrently, biggest first so the
// slowest one starts at once. Each compression holds an xz encoder of up to
// 50 MB, hence the small pool.
func compressDebIndexes(ctx context.Context, indexes []*debIndex) error {
	order := slices.Clone(indexes)
	slices.SortStableFunc(order, func(a, b *debIndex) int { return cmp.Compare(b.size, a.size) })

	var wg errgroup.Group

	wg.SetLimit(debCompressWorkers)

	for _, idx := range order {
		wg.Go(func() error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("group worker: compressing %s: %w", idx.base, err)
			}

			return idx.compress()
		})
	}

	return wg.Wait() //nolint:wrapcheck // the tasks wrap
}

func (c debContribution) hostedEntries() []models.DebPackageEntry {
	if c.hosted == nil {
		return nil
	}

	return c.hosted.entries
}

// debStanzaKey identifies a package version within an index.
type debStanzaKey struct{ pkg, version, arch string }

// acceptDebStanza returns the text of st unless an earlier member provides the
// same package version, hosted-wins hides it, or its file cannot be served
// unambiguously.
func (j *groupJob) acceptDebStanza(seen map[debStanzaKey]struct{}, m *models.Repository, st formatdeb.Stanza, hosted bool) (string, bool) {
	key := debStanzaKey{st.Package, st.Version, st.Architecture}
	if _, dup := seen[key]; dup {
		return "", false
	}

	if !hosted && j.hostedNames != nil {
		if _, hidden := j.hostedNames[st.Package]; hidden {
			return "", false
		}
	}

	file := st.Filename
	if !strings.HasPrefix(file, "pool/") || !pathseg.Path(file) {
		return "", false
	}

	if !j.claim(file, m.ID, st.SHA256) {
		return "", false
	}

	seen[key] = struct{}{}

	return st.Text, true
}

// scanProxyIndex reads the entries of a proxy member's Packages index into scan,
// trying the compressions its Release lists best-first. An index the upstream
// lacks contributes nothing; any other failure is recorded and makes the
// generation partial. It is safe to run concurrently for different scans.
func (j *groupJob) scanProxyIndex(ctx context.Context, c debContribution, dist, comp, arch string, scan *debProxyScan) {
	for _, variant := range c.rel.PackagesVariants(comp, arch) {
		rel := "dists/" + dist + "/" + variant

		rc, err := j.w.opener.Open(ctx, c.member, rel)
		if errors.Is(err, proxy.ErrNotFound) {
			continue
		}

		if err != nil {
			scan.errs = append(scan.errs, debFetchError{rel, err})
			return
		}

		dec, err := formatdeb.DecompressIndex(rc, variant)
		if err != nil {
			scan.errs = append(scan.errs, debFetchError{rel, err})
			return
		}

		// A malformed or truncated upstream index keeps the entries read so
		// far and makes the generation partial, redone soon.
		err = formatdeb.ScanPackages(dec, func(st formatdeb.Stanza) error {
			scan.stanzas = append(scan.stanzas, st)
			return nil
		})
		_ = dec.Close()

		if err != nil {
			scan.errs = append(scan.errs, debFetchError{rel, err})
		}

		return
	}
}
