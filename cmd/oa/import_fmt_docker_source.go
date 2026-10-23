// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// The source side of a docker import speaks the Docker Registry v2 API of the
// source system: the repository listing only says which tags exist, the
// manifests and blobs are what the registry serves for them. Both Nexus and
// Artifactory expose the API of a hosted repository below a repository path, so
// the same credentials and the same-origin rule of sourceHTTP apply.

const (
	ociMediaDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	ociMediaDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociMediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	ociMediaOCIIndex       = "application/vnd.oci.image.index.v1+json"

	// ociMaxManifestBytes is the largest manifest the destination accepts.
	ociMaxManifestBytes = 4 << 20
	// ociMaxIndexDepth bounds nested manifest lists, which no real image uses
	// beyond one level.
	ociMaxIndexDepth = 4

	ociDigestPrefix = "sha256:"
	ociDigestHexLen = 64
)

var (
	errOCIManifest = errors.New("unsupported or invalid manifest")
	errOCINoAPI    = errors.New("the source system does not offer a registry API for its docker repositories")
)

// ociManifestTypes are the manifest media types the importer can copy; they
// are what it asks the source registry for.
var ociManifestTypes = []string{ociMediaDockerManifest, ociMediaDockerList, ociMediaOCIManifest, ociMediaOCIIndex} //nolint:gochecknoglobals // constant list

// ociSource is implemented by the sources whose docker repositories can be
// read through a registry API.
type ociSource interface {
	// registry returns the registry API of the hosted repository repo.
	registry(repo string) ociRegistry
}

// registry serves a Nexus docker repository below its repository path.
func (n *nexusSource) registry(repo string) ociRegistry {
	return ociRegistry{http: n.http, base: n.http.resolve("/repository/" + url.PathEscape(repo) + "/v2")}
}

// registry serves an Artifactory docker repository below /api/docker.
func (a *artifactorySource) registry(repo string) ociRegistry {
	return ociRegistry{http: a.http, base: a.http.resolve("/api/docker/" + url.PathEscape(repo) + "/v2")}
}

// ociRegistry reads manifests and blobs from the source's registry API.
type ociRegistry struct {
	http *sourceHTTP
	// base is the URL of the registry's /v2 root, without a trailing slash.
	base string
}

func (r ociRegistry) url(image, kind, ref string) string {
	return r.base + "/" + escapePath(image) + "/" + kind + "/" + url.PathEscape(ref)
}

// ociDescriptor is a content descriptor of a manifest.
type ociDescriptor struct {
	MediaType string   `json:"mediaType"` //nolint:tagliatelle // OCI/Docker wire format is camelCase
	Digest    string   `json:"digest"`
	Size      int64    `json:"size"`
	URLs      []string `json:"urls"`
}

// foreign reports layers whose bytes live elsewhere (Windows base layers):
// they are never pushed to a registry, so the destination does not hold them.
func (d ociDescriptor) foreign() bool {
	return len(d.URLs) > 0 ||
		d.MediaType == "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip" ||
		strings.Contains(d.MediaType, ".nondistributable.")
}

// ociDocument is the union of the manifest shapes the importer copies: image
// manifest and OCI artifact manifest (config plus layers or blobs) and index
// or manifest list (manifests).
type ociDocument struct {
	SchemaVersion int             `json:"schemaVersion"` //nolint:tagliatelle // OCI/Docker wire format is camelCase
	MediaType     string          `json:"mediaType"`     //nolint:tagliatelle // OCI/Docker wire format is camelCase
	Config        *ociDescriptor  `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
	Blobs         []ociDescriptor `json:"blobs"`
	Manifests     []ociDescriptor `json:"manifests"`
	// Subject is the manifest an OCI 1.1 artifact (signature, SBOM,
	// attestation) is attached to: the manifest is a referrer of it.
	Subject  *ociDescriptor    `json:"subject"`
	FSLayers []json.RawMessage `json:"fsLayers"` //nolint:tagliatelle // Docker schema 1 wire format is camelCase
}

// payload lists the blobs a client must push for the manifest: the config and
// every layer that is not foreign.
func (d ociDocument) payload() []ociDescriptor {
	var out []ociDescriptor

	if d.Config != nil {
		out = append(out, *d.Config)
	}

	for _, group := range [][]ociDescriptor{d.Layers, d.Blobs} {
		for _, l := range group {
			if !l.foreign() {
				out = append(out, l)
			}
		}
	}

	return out
}

// ociManifest is a manifest exactly as the source served it.
type ociManifest struct {
	// Digest is "sha256:<hex>" of Body.
	Digest    string
	MediaType string
	Body      []byte
	doc       ociDocument
}

func ociDigestOf(b []byte) string {
	sum := sha256.Sum256(b)

	return ociDigestPrefix + hex.EncodeToString(sum[:])
}

// ociValidDigest reports whether d is a lower-case sha256 digest, the only
// kind the destination stores; it also keeps source-supplied values out of URL
// paths unless they have the expected shape.
func ociValidDigest(d string) bool {
	hexPart, ok := strings.CutPrefix(d, ociDigestPrefix)
	if !ok || len(hexPart) != ociDigestHexLen {
		return false
	}

	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// acceptTransport adds an Accept header to every request that goes through it.
type acceptTransport struct {
	next   http.RoundTripper
	accept string
}

func (t acceptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Accept", t.accept)

	return t.next.RoundTrip(req)
}

// withAccept returns a copy of s whose requests carry the Accept header, going
// through the same credential handling as s itself.
func (s *sourceHTTP) withAccept(accept string) *sourceHTTP {
	next := s.client.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	client := *s.client
	client.Transport = acceptTransport{next: next, accept: accept}

	cp := *s
	cp.client = &client

	return &cp
}

// manifest fetches the manifest ref (a tag or a digest) of image. The digest is
// computed from the bytes received; a registry that names another one in
// Docker-Content-Digest, or a digest reference that does not match, is refused.
func (r ociRegistry) manifest(ctx context.Context, image, ref string) (ociManifest, error) {
	var m ociManifest

	err := r.http.retry.do(ctx, "GET manifest "+image+":"+ref, func(int) error {
		var err error

		m, err = r.manifestOnce(ctx, image, ref)

		return err
	})

	return m, err
}

func (r ociRegistry) manifestOnce(ctx context.Context, image, ref string) (ociManifest, error) {
	target := r.url(image, "manifests", ref)

	resp, err := r.http.withAccept(strings.Join(ociManifestTypes, ", ")).do(ctx, http.MethodGet, target, nil, "")
	if err != nil {
		return ociManifest{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ociManifest{}, fmt.Errorf("%w: manifest %s:%s", errNotFound, image, ref)
	default:
		return ociManifest{}, statusError(resp, "GET manifest "+image+":"+ref)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ociMaxManifestBytes+1))
	if err != nil {
		return ociManifest{}, err
	}

	if len(body) > ociMaxManifestBytes {
		return ociManifest{}, fmt.Errorf("%w: %s:%s is larger than %d bytes", errOCIManifest, image, ref, ociMaxManifestBytes)
	}

	return parseOCIManifest(image, ref, body, resp.Header)
}

func parseOCIManifest(image, ref string, body []byte, hdr http.Header) (ociManifest, error) {
	m := ociManifest{Digest: ociDigestOf(body), Body: body}

	if said := hdr.Get("Docker-Content-Digest"); said != "" && said != m.Digest {
		return ociManifest{}, fmt.Errorf("%w: %s:%s: the registry names digest %s but served content of %s",
			errImportVerify, image, ref, said, m.Digest)
	}

	if strings.Contains(ref, ":") && ref != m.Digest {
		return ociManifest{}, fmt.Errorf("%w: %s@%s: the registry served content of %s", errImportVerify, image, ref, m.Digest)
	}

	if err := json.Unmarshal(body, &m.doc); err != nil {
		return ociManifest{}, fmt.Errorf("%w: %s:%s is not JSON: %w", errOCIManifest, image, ref, err)
	}

	if m.doc.SchemaVersion == 1 || len(m.doc.FSLayers) > 0 {
		return ociManifest{}, fmt.Errorf("%w: %s:%s is a Docker schema 1 manifest; re-push the image with a current client first",
			errOCIManifest, image, ref)
	}

	mediaType, _, _ := strings.Cut(hdr.Get("Content-Type"), ";")

	switch {
	case isOCIManifestType(strings.TrimSpace(mediaType)):
		m.MediaType = strings.TrimSpace(mediaType)
	case isOCIManifestType(m.doc.MediaType):
		m.MediaType = m.doc.MediaType
	default:
		return ociManifest{}, fmt.Errorf("%w: %s:%s has no recognised media type (Content-Type %q, mediaType %q)",
			errOCIManifest, image, ref, mediaType, m.doc.MediaType)
	}

	return m, nil
}

func isOCIManifestType(t string) bool {
	return slices.Contains(ociManifestTypes, t)
}

// openBlob streams a blob of image from the source registry.
func (r ociRegistry) openBlob(ctx context.Context, image, digest string) (io.ReadCloser, error) {
	return r.http.open(ctx, r.url(image, "blobs", digest))
}

// ociImage is everything a tag stands for, in the order it must be pushed.
type ociImage struct {
	// root is the manifest the tag points at; it is also the last of manifests.
	root ociManifest
	// manifests holds the child manifests of an index before the index itself.
	manifests []ociManifest
	// blobs are the distinct config and layer blobs of all the manifests.
	blobs []ociDescriptor
}

// image resolves a tag into its manifest tree: the tagged manifest, the child
// manifests when it is an index, and the blobs they reference.
func (r ociRegistry) image(ctx context.Context, image, tag string) (ociImage, error) {
	root, err := r.manifest(ctx, image, tag)
	if err != nil {
		return ociImage{}, err
	}

	return r.imageOf(ctx, image, tag, root)
}

// imageOf resolves a manifest that was already fetched as ref: the child
// manifests when it is an index, and the blobs they reference.
func (r ociRegistry) imageOf(ctx context.Context, image, ref string, root ociManifest) (ociImage, error) {
	b := &ociTreeBuilder{reg: r, image: image, manifests: map[string]bool{}, blobs: map[string]bool{}}

	if err := b.addFetched(ctx, ref, root, 0); err != nil {
		return ociImage{}, err
	}

	return ociImage{root: root, manifests: b.out.manifests, blobs: b.out.blobs}, nil
}

type ociTreeBuilder struct {
	reg       ociRegistry
	image     string
	manifests map[string]bool
	blobs     map[string]bool
	out       ociImage
}

// addFetched records the manifest m, which was fetched as ref, after its
// children (fetched here), so children precede their parents in the result.
func (b *ociTreeBuilder) addFetched(ctx context.Context, ref string, m ociManifest, depth int) error {
	if depth > ociMaxIndexDepth {
		return fmt.Errorf("%w: %s: manifest lists are nested deeper than %d levels", errOCIManifest, b.image, ociMaxIndexDepth)
	}

	b.manifests[m.Digest] = true

	for _, child := range m.doc.Manifests {
		if !ociValidDigest(child.Digest) {
			return fmt.Errorf("%w: %s:%s lists a child with the invalid digest %q", errOCIManifest, b.image, ref, child.Digest)
		}

		if b.manifests[child.Digest] {
			continue
		}

		cm, err := b.reg.manifest(ctx, b.image, child.Digest)
		if err != nil {
			return err
		}

		if err := b.addFetched(ctx, child.Digest, cm, depth+1); err != nil {
			return err
		}
	}

	for _, d := range m.doc.payload() {
		if err := b.addBlob(ref, d); err != nil {
			return err
		}
	}

	b.out.manifests = append(b.out.manifests, m)

	return nil
}

func (b *ociTreeBuilder) addBlob(ref string, d ociDescriptor) error {
	if !ociValidDigest(d.Digest) {
		return fmt.Errorf("%w: %s:%s references a blob with the unsupported digest %q", errOCIManifest, b.image, ref, d.Digest)
	}

	if d.Size < 0 {
		return fmt.Errorf("%w: %s:%s: blob %s has a negative size", errOCIManifest, b.image, ref, d.Digest)
	}

	if !b.blobs[d.Digest] {
		b.blobs[d.Digest] = true
		b.out.blobs = append(b.out.blobs, d)
	}

	return nil
}
