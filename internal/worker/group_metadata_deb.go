// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"cmp"
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

	"github.com/klauspost/compress/gzip"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"golang.org/x/sync/errgroup"
)

const (
	// maxUpstreamRelease caps an upstream Release file read for a merge.
	maxUpstreamRelease = 64 << 20
	// debFetchWorkers bounds the concurrent reads of upstream metadata (each
	// holds a decompressor and, on a cold proxy cache, an upstream request).
	debFetchWorkers = 8
	// debCompressWorkers bounds the indexes compressed at once; each holds
	// the accepted stanzas of its index and its output.
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
	files         []debFile
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

		files, err := j.w.stanzas.files(ctx, db, m.ID)
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

		for _, f := range files {
			if hd := byDist[f.stamp.Distribution]; hd != nil {
				hd.files = append(hd.files, f)
			}

			j.hostedNames[f.stamp.Name] = struct{}{}
		}

		out[m.ID] = byDist
	}

	return out, nil
}

// buildDeb generates every distribution of the group: the hosted members' ones
// and the extra ones (named by an earlier request) that some member has.
func (j *groupJob) buildDeb(ctx context.Context, extra, known []string) error {
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

	var unfound []string

	for _, dist := range ordered {
		found, err := j.buildDebDist(ctx, dist, hosted)
		if err != nil {
			return err
		}

		if found {
			j.dists = append(j.dists, dist)
		} else {
			unfound = append(unfound, dist)
		}
	}

	// A distribution whose proxy members could not be read keeps its previous
	// files for the retry.
	if j.partial {
		for _, dist := range unfound {
			if slices.Contains(known, dist) {
				j.dists = append(j.dists, dist)
				j.keepDirs = append(j.keepDirs, "dists/"+dist+"/")
			}
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

		rel := "dists/" + dist + "/" + idx.base + ".gz"

		if idx.reusedGz != nil {
			j.unchanged[rel] = idx.reusedGz.SHA256
			files = append(files, *idx.reusedGz)

			continue
		}

		j.blobs[rel] = idx.gz
		md5sum, sha1sum, sha256sum := formatdeb.CalculateChecksums(idx.gz)
		files = append(
			files,
			formatdeb.FileInfo{
				Filename: idx.base + ".gz",
				Size:     int64(len(idx.gz)),
				MD5sum:   md5sum,
				SHA1:     sha1sum,
				SHA256:   sha256sum,
			},
		)
	}

	sort.Slice(files, func(a, b int) bool { return files[a].Filename < files[b].Filename })

	release := []byte(releaseGen.GenerateRelease(files))
	j.blobs["dists/"+dist+"/Release"] = release

	if j.signer != nil {
		if err := addSignedRelease(j.blobs, "dists/"+dist+"/", release, j.signer); err != nil {
			return false, fmt.Errorf("group worker: %s/%s: %w", j.group.Name, dist, err)
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
	gz   []byte
	// reusedGz is set instead of gz when the index is byte for byte the one the
	// group published last: the stored compressed file is kept and only its
	// Release entry is carried over.
	reusedGz *formatdeb.FileInfo

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

// compress produces the gzip form and the checksums of the plain index, unless
// prev shows that the group already published this very index: then nothing is
// compressed, the stored file stays and its Release entry is the one of the
// previous Release. The compression and the checksums are independent passes
// over the texts, so they run concurrently. Only gzip is published: the pure-Go
// xz encoder costs about ten times the CPU of gzip for an index 5 % smaller,
// and apt uses gzip when Release lists no xz.
func (i *debIndex) compress(prev *debPrevious) error {
	if i.reuse(prev) {
		return nil
	}

	var (
		gzBuf bytes.Buffer
		wg    errgroup.Group
	)

	gzBuf.Grow(int(min(i.size/4, maxBufferHint)))

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

	i.gz = gzBuf.Bytes()
	i.texts = nil

	return nil
}

// reuse sets the index up from prev when the group already published exactly
// this index; it reports whether it did. An index of another size cannot be it,
// so only then is the plain index hashed.
func (i *debIndex) reuse(prev *debPrevious) bool {
	if prev == nil || prev.files[i.base].Size != i.size {
		return false
	}

	h := sha256.New()
	if err := writeAll(h, i.texts); err != nil {
		return false
	}

	sum := hex.EncodeToString(h.Sum(nil))

	plain, gz, ok := prev.reusable(i.base, i.size, sum)
	if !ok {
		return false
	}

	i.plainMD5, i.plainSHA1, i.plainSHA256 = plain.MD5sum, plain.SHA1, plain.SHA256
	i.reusedGz = &gz
	i.texts = nil

	return true
}

// debPrevious is what the group published for a distribution the last time:
// the files its Release listed, and the checksum each stored file is under.
type debPrevious struct {
	files map[string]formatdeb.FileInfo
	// stored is the blob checksum of the stored files by group-relative path.
	stored map[string]string
	// prefix is the group-relative path of the distribution's directory,
	// "dists/<dist>/", under the scope.
	prefix string
}

// reusable returns the Release entries of the index base (relative to the
// distribution) and of its gzip form when the last published index has the given
// size and SHA-256 and its gzip file is still stored as that Release lists it.
func (p *debPrevious) reusable(
	base string,
	size int64,
	sha256sum string,
) (formatdeb.FileInfo, formatdeb.FileInfo, bool) {
	plain, gz := p.files[base], p.files[base+".gz"]
	if plain.SHA256 != sha256sum || plain.Size != size {
		return plain, gz, false
	}

	ok := p.stored[p.prefix+base+".gz"] == gz.SHA256 && gz.SHA256 != ""

	return plain, gz, ok
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
			indexes = append(indexes, &debIndex{base: fmt.Sprintf("%s/binary-%s/Packages", comp, arch)})
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

	if err := compressDebIndexes(ctx, indexes, j.previousDebFiles(ctx, dist)); err != nil {
		return nil, err
	}

	return indexes, nil
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
			for i := range c.hosted.files {
				e := &c.hosted.files[i].stamp
				if e.ComponentName != comp || (e.Arch != arch && e.Arch != "all") {
					continue
				}

				st := formatdeb.Stanza{
					Package: e.Name, Version: e.Version, Architecture: e.Arch,
					Filename: formatdeb.GetPoolPath(e.ComponentName, e.Name, e.Version, e.Filename),
					SHA256:   e.SHA256, Text: c.hosted.files[i].text,
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
// slowest one starts at once. Each compression holds the index's stanzas and
// its output, hence the small pool.
func compressDebIndexes(ctx context.Context, indexes []*debIndex, prev *debPrevious) error {
	order := slices.Clone(indexes)
	slices.SortStableFunc(order, func(a, b *debIndex) int { return cmp.Compare(b.size, a.size) })

	var wg errgroup.Group

	wg.SetLimit(debCompressWorkers)

	for _, idx := range order {
		wg.Go(func() error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("group worker: compressing %s: %w", idx.base, err)
			}

			return idx.compress(prev)
		})
	}

	return wg.Wait() //nolint:wrapcheck // the tasks wrap
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

// previousDebFiles reads what the group published for dist the last time (nil
// when it published nothing, or it cannot be read: the index is then compressed
// afresh).
func (j *groupJob) previousDebFiles(ctx context.Context, dist string) *debPrevious {
	prefix := "dists/" + dist + "/"

	stored := j.storedDebMetadata(ctx)

	sum := stored[prefix+"Release"]
	if sum == "" {
		return nil
	}

	rc, err := j.w.blobStore.Get(ctx, sum)
	if err != nil {
		return nil
	}

	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, maxUpstreamRelease))
	if err != nil {
		return nil
	}

	return &debPrevious{files: formatdeb.ParseReleaseChecksums(data), stored: stored, prefix: prefix}
}

// storedDebMetadata returns the checksums of the group's stored dists/ files
// (by group-relative path) as they were when the generation started; the group
// lock keeps anyone else from changing them meanwhile.
func (j *groupJob) storedDebMetadata(ctx context.Context) map[string]string {
	if j.stored != nil {
		return j.stored
	}

	j.stored = map[string]string{}

	if j.w.db == nil {
		return j.stored
	}

	db := j.w.db.GetDB()
	if db == nil {
		return j.stored
	}

	rows, err := db.QueryContext(ctx,
		`SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1 AND starts_with(path, $2)`,
		j.group.ID, j.scope+"dists/")
	if err != nil {
		return j.stored
	}

	defer func() { _ = rows.Close() }()

	found := map[string]string{}

	for rows.Next() {
		var path, sum string
		if err := rows.Scan(&path, &sum); err != nil {
			return j.stored
		}

		found[strings.TrimPrefix(path, j.scope)] = sum
	}

	if rows.Err() == nil {
		j.stored = found
	}

	return j.stored
}
