// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	npmTarballExt = ".tgz"
	// npmAttachmentType is the only content type the publish endpoint accepts
	// for a tarball attachment.
	npmAttachmentType = "application/octet-stream"
	// npmMaxPublishBody is the server's cap on a publish request (200 MiB): the
	// whole packument, with the tarball base64 encoded inside it.
	npmMaxPublishBody = 200 << 20
	// npmMaxManifest bounds the package.json read out of a tarball.
	npmMaxManifest = 8 << 20
)

var (
	errNPMManifest = errors.New("cannot read package.json from the tarball")
	errNPMIdentity = errors.New("tarball does not match its path in the source")
	errNPMTooLarge = errors.New("tarball is too large for the publish endpoint")

	npmSemverRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
)

func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format: formatNPM, nexus: []string{"npm"}, artifactory: []string{"npm"},
		importable: importableNPM, match: matchSHA,
		units: npmUnits, upload: npmUpload, remove: npmRemove, transferUnit: npmTransferUnit,
	})
}

// npmCoordinates reads a package name and version out of a tarball path:
//
//	<pkg>/-/<pkg>-<ver>.tgz                      (Nexus, Artifactory)
//	@scope/<pkg>/-/<pkg>-<ver>.tgz               (Nexus)
//	@scope/<pkg>/-/@scope/<pkg>-<ver>.tgz        (Artifactory)
//
// Packuments, Artifactory's .npm/ metadata and anything else yield false.
func npmCoordinates(p string) (string, string, bool) {
	pkg, file, found := strings.Cut(p, "/-/")
	if !found {
		return "", "", false
	}

	scope, unscoped, scoped := strings.Cut(pkg, "/")
	if !scoped {
		scope, unscoped = "", pkg
	} else if !strings.HasPrefix(scope, "@") || scope == "@" {
		return "", "", false
	}

	if unscoped == "" || strings.Contains(unscoped, "/") {
		return "", "", false
	}

	// Artifactory repeats the scope in front of the file name of scoped packages.
	if dir, base, nested := strings.Cut(file, "/"); nested {
		if scope == "" || dir != scope {
			return "", "", false
		}

		file = base
	}

	stem, isTarball := strings.CutSuffix(file, npmTarballExt)
	version, hasName := strings.CutPrefix(stem, unscoped+"-")

	if !isTarball || !hasName || version == "" || strings.Contains(file, "/") {
		return "", "", false
	}

	return pkg, version, true
}

func importableNPM(p string) bool {
	_, _, ok := npmCoordinates(p)

	return ok
}

// npmTarballName is the file name of a version's tarball: the unscoped package
// name, a dash and the version.
func npmTarballName(name, version string) string {
	_, unscoped, scoped := strings.Cut(name, "/")
	if !scoped {
		unscoped = name
	}

	return unscoped + "-" + version + npmTarballExt
}

// npmEscapedName is a package name as one URL path segment ("@scope%2Fname").
func npmEscapedName(name string) string { return url.PathEscape(name) }

// ─── Ordering ─────────────────────────────────────────────────────────────────

// npmUnits groups the tarballs per package, oldest version first. The server
// keeps no "latest" of its own on publish (the dist-tag comes from the request),
// and ascending order lets each publish move the tag forward.
func npmUnits(assets []sourceAsset) [][]sourceAsset {
	var order []string

	byPkg := map[string][]sourceAsset{}

	for _, a := range assets {
		name, _, _ := npmCoordinates(a.Path)
		if _, seen := byPkg[name]; !seen {
			order = append(order, name)
		}

		byPkg[name] = append(byPkg[name], a)
	}

	units := make([][]sourceAsset, 0, len(order))

	for _, name := range order {
		unit := byPkg[name]
		slices.SortStableFunc(unit, func(x, y sourceAsset) int {
			_, vx, _ := npmCoordinates(x.Path)
			_, vy, _ := npmCoordinates(y.Path)

			return cmp.Or(npmCompareVersions(vx, vy), strings.Compare(x.Path, y.Path))
		})

		units = append(units, unit)
	}

	return units
}

// npmSemver is a parsed semantic version: the numeric core and the dot-separated
// prerelease identifiers (build metadata does not take part in precedence).
type npmSemver struct {
	core [3]uint64
	pre  []string
}

func npmParseSemver(v string) (npmSemver, bool) {
	m := npmSemverRe.FindStringSubmatch(v)
	if m == nil {
		return npmSemver{}, false
	}

	var s npmSemver

	for i := range s.core {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return npmSemver{}, false
		}

		s.core[i] = n
	}

	if m[4] != "" {
		s.pre = strings.Split(m[4], ".")
	}

	return s, true
}

func (a npmSemver) compare(b npmSemver) int {
	if c := slices.Compare(a.core[:], b.core[:]); c != 0 {
		return c
	}

	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}

	return slices.CompareFunc(a.pre, b.pre, npmComparePrerelease)
}

// npmComparePrerelease orders prerelease identifiers: numbers numerically and
// below words, words lexically.
func npmComparePrerelease(a, b string) int {
	an, aErr := strconv.ParseUint(a, 10, 64)
	bn, bErr := strconv.ParseUint(b, 10, 64)

	switch {
	case aErr == nil && bErr == nil:
		return cmp.Compare(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}

	return strings.Compare(a, b)
}

// npmCompareVersions orders versions by semver precedence (a prerelease before
// its release); text that is no semver version sorts first, and versions that
// differ only in build metadata fall back to their text.
func npmCompareVersions(a, b string) int {
	sa, okA := npmParseSemver(a)
	sb, okB := npmParseSemver(b)

	switch {
	case okA && okB:
		return cmp.Or(sa.compare(sb), strings.Compare(a, b))
	case okA:
		return 1
	case okB:
		return -1
	}

	return strings.Compare(a, b)
}

// npmReplacesLatest reports whether publishing version should move the "latest"
// dist-tag away from current: a release always beats a prerelease and, within
// the same kind, the higher version wins (the rule of the server's own latest
// computation), so the final tag does not depend on the order versions arrive.
func npmReplacesLatest(version, current string) bool {
	if current == "" {
		return true
	}

	sv, okV := npmParseSemver(version)
	sc, okC := npmParseSemver(current)

	switch {
	case !okV:
		return false
	case !okC:
		return true
	}

	if relV, relC := len(sv.pre) == 0, len(sc.pre) == 0; relV != relC {
		return relV
	}

	return sv.compare(sc) > 0
}

// ─── Tarball ──────────────────────────────────────────────────────────────────

// npmIsManifestEntry reports whether a tar entry is the package's own
// package.json: "<top directory>/package.json" (normally "package/").
func npmIsManifestEntry(name string) bool {
	top, rest, ok := strings.Cut(strings.TrimLeft(path.Clean(name), "/"), "/")

	return ok && top != "" && top != "." && rest == "package.json"
}

// npmReadManifest returns the members of the first top-level package.json of
// an npm tarball.
func npmReadManifest(tgz io.Reader) (map[string]json.RawMessage, error) {
	zr, err := gzip.NewReader(tgz)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNPMManifest, err)
	}

	defer func() { _ = zr.Close() }()

	tr := tar.NewReader(zr)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: the archive has none", errNPMManifest)
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %w", errNPMManifest, err)
		}

		if hdr.Typeflag != tar.TypeReg || !npmIsManifestEntry(hdr.Name) {
			continue
		}

		raw, err := io.ReadAll(io.LimitReader(tr, npmMaxManifest+1))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errNPMManifest, err)
		}

		if len(raw) > npmMaxManifest {
			return nil, fmt.Errorf("%w: it is larger than %d bytes", errNPMManifest, npmMaxManifest)
		}

		var manifest map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &manifest); err != nil {
			return nil, fmt.Errorf("%w: %w", errNPMManifest, err)
		}

		return manifest, nil
	}
}

// npmManifestIdentity reads the package name and version a manifest declares.
func npmManifestIdentity(manifest map[string]json.RawMessage) (string, string, error) {
	var name, version string

	if err := json.Unmarshal(manifest["name"], &name); err != nil || name == "" {
		return "", "", fmt.Errorf("%w: package.json has no name", errNPMManifest)
	}

	if err := json.Unmarshal(manifest["version"], &version); err != nil || version == "" {
		return "", "", fmt.Errorf("%w: package.json has no version", errNPMManifest)
	}

	return name, version, nil
}

// ─── Publish ──────────────────────────────────────────────────────────────────

// npmPublish is the npm publish document of one version: the packument with the
// tarball base64 encoded into its _attachments. Only the head and tail are held
// in memory; the tarball is encoded on the fly while the request is sent.
type npmPublish struct {
	head, tail []byte
	tarball    io.Reader
	size       int64
}

// npmDocument is the shape of the publish request (the field names are the
// npm registry protocol's); _attachments comes last so that its data, the
// base64 tarball, ends the document.
type npmDocument struct {
	ID          string                     `json:"_id"` //nolint:tagliatelle // npm registry protocol field name
	Name        string                     `json:"name"`
	DistTags    map[string]string          `json:"dist-tags"` //nolint:tagliatelle // npm registry protocol field name
	Versions    map[string]json.RawMessage `json:"versions"`
	Attachments map[string]npmAttachment   `json:"_attachments"` //nolint:tagliatelle // npm registry protocol field name
}

type npmAttachment struct {
	ContentType string `json:"content_type"`
	Length      int64  `json:"length"`
	Data        string `json:"data"`
}

// newNPMPublish lays out the publish document of a version; latest adds the
// "latest" dist-tag. The document is marshaled with an empty data string, and
// cut open at the data's quotes: what precedes them is the head, `"}}}` the tail.
func newNPMPublish(name, version string, manifest []byte, latest bool, tarball io.Reader, size int64) (*npmPublish, error) {
	doc := npmDocument{
		ID: name, Name: name, DistTags: map[string]string{},
		Versions:    map[string]json.RawMessage{version: manifest},
		Attachments: map[string]npmAttachment{name + "-" + version + npmTarballExt: {ContentType: npmAttachmentType, Length: size}},
	}

	if latest {
		doc.DistTags["latest"] = version
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("building the publish document of %s@%s: %w", name, version, err)
	}

	cut := len(raw) - len(`"}}}`)

	return &npmPublish{head: raw[:cut], tail: raw[cut:], tarball: tarball, size: size}, nil
}

// length is the exact size of the request body.
func (p *npmPublish) length() int64 {
	return int64(len(p.head)) + (p.size+2)/3*4 + int64(len(p.tail))
}

// open starts encoding the document; the caller must close the reader.
func (p *npmPublish) open() io.ReadCloser {
	pr, pw := io.Pipe()

	go func() { _ = pw.CloseWithError(p.write(pw)) }()

	return pr
}

func (p *npmPublish) write(w io.Writer) error {
	if _, err := w.Write(p.head); err != nil {
		return err
	}

	enc := base64.NewEncoder(base64.StdEncoding, w)

	if _, err := io.Copy(enc, p.tarball); err != nil {
		return err
	}

	if err := enc.Close(); err != nil {
		return err
	}

	_, err := w.Write(p.tail)

	return err
}

// npmDistTags reads the dist-tags of a package in the destination (none when
// the package does not exist yet).
func npmDistTags(ctx context.Context, t *importTarget, dst, name string) (map[string]string, error) {
	var tags map[string]string

	status, err := t.getJSON(ctx, "/repos/"+url.PathEscape(dst)+"/-/package/"+npmEscapedName(name)+"/dist-tags", &tags)

	switch {
	case status == http.StatusNotFound:
		return map[string]string{}, nil
	case err != nil:
		return nil, fmt.Errorf("reading the dist-tags of %s: %w", name, err)
	}

	return tags, nil
}

// npmCurrentLatest reads the "latest" dist-tag of a package ("" when the
// package or the tag does not exist yet).
func npmCurrentLatest(ctx context.Context, t *importTarget, dst, name string) (string, error) {
	tags, err := npmDistTags(ctx, t, dst, name)
	if err != nil {
		return "", err
	}

	return tags["latest"], nil
}

// npmPrepared is a spooled tarball with the identity its package.json declares.
type npmPrepared struct {
	name, version string
	manifest      []byte
}

// npmPrepare reads the manifest of a spooled tarball and checks that it is the
// package and version the source's path says it is: the copy must not land under
// other coordinates than the source lists it under.
func npmPrepare(sp *spooled, assetPath string) (npmPrepared, error) {
	pathName, pathVersion, _ := npmCoordinates(assetPath)

	manifest, err := npmReadManifest(sp.Reader())
	if err != nil {
		return npmPrepared{}, err
	}

	name, version, err := npmManifestIdentity(manifest)
	if err != nil {
		return npmPrepared{}, err
	}

	if name != pathName || version != pathVersion {
		return npmPrepared{}, fmt.Errorf("%w: package.json declares %s@%s, the path says %s@%s", errNPMIdentity, name, version, pathName, pathVersion)
	}

	// dist belongs to the registry that serves the version; a stray one in
	// package.json would only make the integrity check of the server fail.
	delete(manifest, "dist")

	raw, err := json.Marshal(manifest)
	if err != nil {
		return npmPrepared{}, fmt.Errorf("%w: %w", errNPMManifest, err)
	}

	return npmPrepared{name: name, version: version, manifest: raw}, nil
}

func npmUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	digest := sha256.New()

	sp, err := spool(io.TeeReader(r.Body, digest))
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = sp.Close() }()

	pkg, err := npmPrepare(sp, r.Asset.Path)
	if err != nil {
		return uploadResult{}, err
	}

	current, err := npmCurrentLatest(ctx, t, r.Dst, pkg.name)
	if err != nil {
		return uploadResult{}, err
	}

	doc, err := newNPMPublish(pkg.name, pkg.version, pkg.manifest, npmReplacesLatest(pkg.version, current), sp.Reader(), sp.Size)
	if err != nil {
		return uploadResult{}, err
	}

	if doc.length() > npmMaxPublishBody {
		return uploadResult{}, fmt.Errorf("%w: %d bytes (the limit is a %d byte publish request, base64 encoded)", errNPMTooLarge, sp.Size, npmMaxPublishBody)
	}

	body := doc.open()

	defer func() { _ = body.Close() }()

	hdr := r.Meta.header()
	hdr.Set("Content-Type", "application/json")

	resp, err := t.send(ctx, http.MethodPut, "/repos/"+url.PathEscape(r.Dst)+"/"+npmEscapedName(pkg.name), body, doc.length(), hdr)

	switch {
	case errors.Is(err, errUploadConflict):
		return npmExisting(ctx, t, r.Dst, pkg, hex.EncodeToString(digest.Sum(nil)))
	case err != nil:
		return uploadResult{}, err
	}

	_ = resp.Body.Close()

	return npmStored(ctx, t, r.Dst, pkg)
}

// npmStored reads the id and digest of a published version from the listing:
// the publish reply carries neither.
func npmStored(ctx context.Context, t *importTarget, dst string, pkg npmPrepared) (uploadResult, error) {
	return t.storedResult(ctx, dst, formatNPM, pkg.name, func(p listedPackage) bool {
		return p.Name == pkg.name && p.Version == pkg.version
	})
}

// npmExisting settles a "version already exists" answer: when the published
// tarball is the very one that was sent (sources that report no SHA-256, like
// Nexus npm repositories, cannot be matched against the destination up front, and
// a publish whose reply was lost leaves the version behind) there is nothing
// left to do; any other content is a genuine conflict.
func npmExisting(ctx context.Context, t *importTarget, dst string, pkg npmPrepared, sentSHA256 string) (uploadResult, error) {
	res, err := npmStored(ctx, t, dst, pkg)
	if err != nil {
		return uploadResult{}, fmt.Errorf("%w (looking up the published copy: %w)", errUploadConflict, err)
	}

	if res.SHA256 != sentSHA256 {
		return uploadResult{}, errUploadConflict
	}

	return res, nil
}

// npmRemove unpublishes a version that failed verification
// (DELETE /<pkg>/-/<file>.tgz/-rev/<rev>; the server ignores the revision).
// Versions older than 72 hours can only be unpublished in repositories that
// allow full unpublish, which a back-dated import hits for most packages.
func npmRemove(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
	name, version, _ := npmCoordinates(a.Path)

	return t.deletePath(ctx, "/repos/"+url.PathEscape(dst)+"/"+npmEscapedName(name)+"/-/"+url.PathEscape(npmTarballName(name, version))+"/-rev/1")
}
