// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

const (
	// maxUpstreamRepomd caps an upstream repomd.xml read for a merge.
	maxUpstreamRepomd = 16 << 20
	// rpmXMLPerPackageHint sizes the xz dictionary of the merged documents.
	rpmXMLPerPackageHint = 2048
)

// rpmDocKinds are the repodata documents a group merges, in repomd order.
func rpmDocKinds() []rpmDocKind {
	return []rpmDocKind{
		{"primary", formatrpm.DocPrimary},
		{"filelists", formatrpm.DocFilelists},
		{"other", formatrpm.DocOther},
	}
}

type rpmDocKind struct {
	name string
	kind formatrpm.DocKind
}

// rpmSource is one member's contribution to the merged repodata.
type rpmSource struct {
	member *models.Repository
	// hosted holds a hosted member's packages, selected those of them that
	// make it into the merge.
	hosted   []*pkgrpm.Package
	selected []*pkgrpm.Package
	// hrefs are an upstream proxy's repodata files by type, keep the package
	// checksums selected from its primary.
	hrefs map[string]string
	keep  map[string]struct{}
}

// nevra is the identity two members' packages collide on.
func nevra(p *pkgrpm.Package) string {
	return formatrpm.PackageInfo{
		Name: p.Name, Arch: p.Arch, Epoch: strconv.FormatUint(uint64(p.Epoch), 10), Ver: p.Version, Rel: p.Release,
	}.NEVRA()
}

// buildRPM renders the merged repodata of the group.
func (j *groupJob) buildRPM(ctx context.Context) error {
	sources, err := j.loadRPMSources(ctx)
	if err != nil {
		return err
	}

	count, err := j.selectRPMPackages(ctx, sources)
	if err != nil {
		return err
	}

	ts := time.Now().Unix()

	var entries []formatrpm.RepomdData

	for _, doc := range rpmDocKinds() {
		comp, err := formatrpm.CompressMetadataStream(func(out io.Writer) error {
			return j.writeRPMDocument(ctx, out, doc.name, doc.kind, sources, count)
		}, count*rpmXMLPerPackageHint)
		if err != nil {
			return fmt.Errorf("group worker: generating %s.xml: %w", doc.name, err)
		}

		loc := fmt.Sprintf("repodata/%s-%s.xml.gz", comp.GzSHA256, doc.name)

		entries = append(entries, formatrpm.RepomdData{
			Type: doc.name, Location: loc, Checksum: comp.GzSHA256, ChecksumType: "sha256",
			Timestamp: ts, Size: comp.GzSize, OpenSize: comp.OpenSize,
		})

		j.blobs[loc] = comp.GzipData
		j.blobs[fmt.Sprintf("repodata/%s-%s.xml.xz", comp.XZSHA256, doc.name)] = comp.XZData
	}

	if entry, data, ok, err := j.mergeModules(ctx, sources, ts); err != nil {
		return err
	} else if ok {
		entries = append(entries, entry)
		j.blobs[entry.Location] = data
	}

	repomd, err := formatrpm.GenerateRepomdXML(strconv.FormatInt(ts, 10), entries)
	if err != nil {
		return fmt.Errorf("group worker: generating repomd.xml: %w", err)
	}

	j.blobs["repodata/repomd.xml"] = repomd

	if j.signer != nil {
		sig, err := formatrpm.SignRepomdXML(j.signer, repomd)
		if err != nil {
			j.w.log.Warnw("group worker: failed to sign repomd.xml", "group", j.group.Name, "error", err)
		} else {
			j.blobs["repodata/repomd.xml.asc"] = sig
		}

		j.addPublicKey()
	}

	return nil
}

// loadRPMSources reads every member: hosted ones from the database, proxy ones
// by their upstream repomd.xml. A proxy member whose repodata cannot be read is
// left out of this generation, which is then partial.
func (j *groupJob) loadRPMSources(ctx context.Context) ([]*rpmSource, error) {
	if !j.group.MergeProxyVersions {
		j.hostedNames = make(map[string]struct{})
	}

	var sources []*rpmSource

	for _, m := range j.members {
		if m.RepoType == repotype.Proxy {
			hrefs, err := j.fetchRepomd(ctx, m)
			if err != nil {
				j.noteFetchError(m, "repodata/repomd.xml", err)
				continue
			}

			if hrefs != nil {
				sources = append(sources, &rpmSource{member: m, hrefs: hrefs, keep: make(map[string]struct{})})
			}

			continue
		}

		pkgs, err := j.w.rpm.PackagesForMetadata(ctx, m.ID)
		if err != nil {
			return nil, fmt.Errorf("group worker: loading packages of %s: %w", m.Name, err)
		}

		src := &rpmSource{member: m}
		for i := range pkgs {
			src.hosted = append(src.hosted, &pkgs[i])

			if j.hostedNames != nil {
				j.hostedNames[pkgs[i].Name] = struct{}{}
			}
		}

		sources = append(sources, src)
	}

	return sources, nil
}

// fetchRepomd reads a proxy member's repomd.xml. nil hrefs with a nil error
// means the upstream has no usable repodata (absent, or a document the merge
// cannot read completely).
func (j *groupJob) fetchRepomd(ctx context.Context, m *models.Repository) (map[string]string, error) {
	data, err := j.w.readAll(ctx, m, "repodata/repomd.xml", maxUpstreamRepomd)
	if errors.Is(err, proxy.ErrNotFound) {
		return nil, nil //nolint:nilnil // absent is not an error
	}

	if err != nil {
		return nil, err
	}

	hrefs, err := formatrpm.ParseRepomd(bytes.NewReader(data))
	if err != nil {
		return nil, err //nolint:wrapcheck // describes itself
	}

	for _, doc := range rpmDocKinds() {
		if hrefs[doc.name] == "" {
			return nil, fmt.Errorf("group worker: repomd.xml of %s has no %s data", m.Name, doc.name) //nolint:err113 // dynamic context
		}
	}

	return hrefs, nil
}

// openRPMDocument opens one upstream repodata file of a proxy source,
// decompressed.
func (j *groupJob) openRPMDocument(ctx context.Context, src *rpmSource, typ string) (io.ReadCloser, error) {
	href, ok := formatrpm.SafeLocation(src.hrefs[typ])
	if !ok {
		return nil, fmt.Errorf("group worker: unsafe %s location %q of %s", typ, src.hrefs[typ], src.member.Name) //nolint:err113 // dynamic context
	}

	rc, err := j.w.opener.Open(ctx, src.member, href)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers classify the sentinel
	}

	return formatrpm.OpenMetadata(rc, href)
}

// selectRPMPackages picks, in member order, the package of each name-epoch-
// version-release-arch (the first member's wins), applies hosted-wins and
// registers the owner of every package file. It returns the number selected.
func (j *groupJob) selectRPMPackages(ctx context.Context, sources []*rpmSource) (int, error) {
	seen := make(map[string]struct{})
	count := 0

	for _, src := range sources {
		if src.hrefs == nil {
			for _, p := range src.hosted {
				id := nevra(p)
				if _, dup := seen[id]; dup {
					continue
				}

				loc, ok := formatrpm.SafeLocation(formatrpm.PackageLocation(p))
				if !ok || !j.claim(loc, src.member.ID, p.SHA256) {
					continue
				}

				seen[id] = struct{}{}
				src.selected = append(src.selected, p)
				count++
			}

			continue
		}

		n, err := j.selectProxyPackages(ctx, src, seen)
		if err != nil {
			return 0, err
		}

		count += n
	}

	return count, nil
}

// selectProxyPackages streams a proxy member's primary and keeps the packages
// that no earlier member provides and hosted-wins does not hide. A read failure
// here fails the generation: a truncated selection would silently drop
// packages from the metadata clients already rely on.
func (j *groupJob) selectProxyPackages(ctx context.Context, src *rpmSource, seen map[string]struct{}) (int, error) {
	rc, err := j.openRPMDocument(ctx, src, "primary")
	if err != nil {
		return 0, fmt.Errorf("group worker: reading primary of %s: %w", src.member.Name, err)
	}

	defer func() { _ = rc.Close() }()

	n := 0

	err = formatrpm.ScanPackages(rc, false, func(info formatrpm.PackageInfo, _ []byte) error {
		if info.PkgID == "" || info.Name == "" {
			return nil
		}

		if j.hostedNames != nil {
			if _, hidden := j.hostedNames[info.Name]; hidden {
				return nil
			}
		}

		id := info.NEVRA()
		if _, dup := seen[id]; dup {
			return nil
		}

		loc, ok := formatrpm.SafeLocation(info.Href)
		if !ok || !j.claim(loc, src.member.ID, info.PkgID) {
			return nil
		}

		seen[id] = struct{}{}
		src.keep[info.PkgID] = struct{}{}
		n++

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("group worker: reading primary of %s: %w", src.member.Name, err)
	}

	return n, nil
}

// writeRPMDocument renders one merged document: hosted packages are rendered,
// proxy members' elements copied from their upstream document.
func (j *groupJob) writeRPMDocument(ctx context.Context, out io.Writer, typ string, kind formatrpm.DocKind, sources []*rpmSource, count int) error {
	d := formatrpm.NewDocWriter(out, kind, count)

	for _, src := range sources {
		if src.hrefs == nil {
			for _, p := range src.selected {
				d.Package(p)
			}

			continue
		}

		if err := j.copyRPMElements(ctx, d, src, typ); err != nil {
			return err
		}
	}

	return d.Close()
}

// copyRPMElements copies the selected packages' elements of a proxy member's
// upstream document into d.
func (j *groupJob) copyRPMElements(ctx context.Context, d *formatrpm.DocWriter, src *rpmSource, typ string) error {
	rc, err := j.openRPMDocument(ctx, src, typ)
	if err != nil {
		return fmt.Errorf("group worker: reading %s of %s: %w", typ, src.member.Name, err)
	}

	defer func() { _ = rc.Close() }()

	remaining := maps.Clone(src.keep)

	err = formatrpm.ScanPackages(rc, true, func(info formatrpm.PackageInfo, element []byte) error {
		if _, ok := remaining[info.PkgID]; !ok {
			return nil
		}

		delete(remaining, info.PkgID)
		d.Raw(element)

		return nil
	})
	if err != nil {
		return fmt.Errorf("group worker: reading %s of %s: %w", typ, src.member.Name, err)
	}

	return nil
}

// mergeModules merges the proxy members' modules.yaml documents. ok is false
// when no member provides any.
func (j *groupJob) mergeModules(ctx context.Context, sources []*rpmSource, ts int64) (formatrpm.RepomdData, []byte, bool, error) {
	var (
		readers []io.Reader
		closers []io.Closer
	)

	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	for _, src := range sources {
		if src.hrefs == nil || src.hrefs["modules"] == "" {
			continue
		}

		rc, err := j.openRPMDocument(ctx, src, "modules")
		if err != nil {
			j.noteFetchError(src.member, src.hrefs["modules"], err)
			continue
		}

		readers, closers = append(readers, rc), append(closers, rc)
	}

	if len(readers) == 0 {
		return formatrpm.RepomdData{}, nil, false, nil
	}

	var merged bytes.Buffer

	n, err := formatrpm.MergeModules(&merged, readers)
	if err != nil {
		return formatrpm.RepomdData{}, nil, false, fmt.Errorf("group worker: merging modules: %w", err)
	}

	if n == 0 {
		return formatrpm.RepomdData{}, nil, false, nil
	}

	var gz bytes.Buffer

	gw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		return formatrpm.RepomdData{}, nil, false, fmt.Errorf("group worker: compressing modules: %w", err)
	}

	if _, err := gw.Write(merged.Bytes()); err != nil {
		return formatrpm.RepomdData{}, nil, false, fmt.Errorf("group worker: compressing modules: %w", err)
	}

	if err := gw.Close(); err != nil {
		return formatrpm.RepomdData{}, nil, false, fmt.Errorf("group worker: compressing modules: %w", err)
	}

	sum := sha256.Sum256(gz.Bytes())
	hexSum := hex.EncodeToString(sum[:])

	return formatrpm.RepomdData{
		Type: "modules", Location: "repodata/" + hexSum + "-modules.yaml.gz",
		Checksum: hexSum, ChecksumType: "sha256", Timestamp: ts, Size: int64(gz.Len()), OpenSize: int64(merged.Len()),
	}, gz.Bytes(), true, nil
}
