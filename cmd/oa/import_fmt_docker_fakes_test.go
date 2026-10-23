// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/ocipath"
)

// ─── fake OpenArtifact registry ───────────────────────────────────────────────

// dockerFakes maps a fakeOA to the state of its docker registry routes.
var dockerFakes sync.Map //nolint:gochecknoglobals // test registry

func init() { //nolint:gochecknoinits // fakeOARoutes are registered from init, see import_fakes_test.go
	fakeOARoutes["docker"] = func(f *fakeOA, mux *http.ServeMux) {
		d := &fakeDocker{
			oa:      f,
			images:  map[string]*fakeDockerImage{},
			uploads: map[string]*fakeDockerUpload{},
			blobPut: map[string]int{},
		}
		dockerFakes.Store(f, d)
		mux.HandleFunc("/v2/", d.serve)
	}
}

// fakeDockerOf returns the docker registry state of a fakeOA.
func fakeDockerOf(f *fakeOA) *fakeDocker {
	v, _ := dockerFakes.Load(f)

	return v.(*fakeDocker)
}

type fakeDockerImage struct {
	blobs     map[string][]byte
	manifests map[string][]byte
	types     map[string]string
	tags      map[string]string
	// subjects maps a stored manifest to the digest it names as its subject.
	subjects map[string]string
}

type fakeDockerUpload struct{ name string }

type fakeManifestPut struct {
	name, ref, contentType, created string
}

// fakeDocker implements the OCI distribution endpoints the importer uses, with
// the checks of OpenArtifact's registry: blobs are visible per image, uploads
// are verified against their digest, manifests need their blobs and children.
type fakeDocker struct {
	oa      *fakeOA
	images  map[string]*fakeDockerImage
	uploads map[string]*fakeDockerUpload

	// events records mutating requests in order: "blob <image>@<digest>",
	// "mount <image>@<digest> from <image>", "manifest <image> <ref>".
	events          []string
	blobPut         map[string]int
	puts            []fakeManifestPut
	deletedManifest []string
	heads           int
	manifestHeads   int
	unexpected      []string

	// Behaviour switches.
	immutable       bool // a tag cannot move to other content (409)
	mangle          bool // tagged manifests are stored altered
	failBlobPuts    int  // the next n blob PUTs answer 503
	ignoreSubject   bool // manifests with a subject are stored without the OCI-Subject answer
	foreignLocation bool // upload sessions are announced with an absolute URL on another host
}

func (d *fakeDocker) image(name string) *fakeDockerImage {
	img := d.images[name]
	if img == nil {
		img = &fakeDockerImage{
			blobs: map[string][]byte{}, manifests: map[string][]byte{},
			types: map[string]string{}, tags: map[string]string{}, subjects: map[string]string{},
		}
		d.images[name] = img
	}

	return img
}

// peek looks an image up without creating it, as reads do.
func (d *fakeDocker) peek(name string) *fakeDockerImage {
	if img := d.images[name]; img != nil {
		return img
	}

	return &fakeDockerImage{blobs: map[string][]byte{}, manifests: map[string][]byte{}, types: map[string]string{}, tags: map[string]string{}, subjects: map[string]string{}}
}

func (d *fakeDocker) fail(w http.ResponseWriter, code int, errCode string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":%q}]}`, errCode, errCode)
}

func (d *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	req := ocipath.Parse(strings.TrimPrefix(r.URL.Path, "/v2/"))
	name := r.Header.Get("X-Fake-Repo") + "/" + req.Name

	switch {
	case req.Kind == ocipath.KindUploadStart && r.Method == http.MethodPost:
		d.startUpload(w, r, name)
	case req.Kind == ocipath.KindUpload && r.Method == http.MethodPut:
		d.finishUpload(w, r, name, req.Ref)
	case req.Kind == ocipath.KindUpload && r.Method == http.MethodDelete:
		d.oa.writes++
		delete(d.uploads, req.Ref)
		w.WriteHeader(http.StatusNoContent)
	case req.Kind == ocipath.KindBlob && r.Method == http.MethodHead:
		d.headBlob(w, name, req.Ref)
	case req.Kind == ocipath.KindManifest && r.Method == http.MethodHead:
		d.headManifest(w, name, req.Ref)
	case req.Kind == ocipath.KindReferrers && r.Method == http.MethodGet:
		d.referrers(w, name, req.Ref)
	case req.Kind == ocipath.KindManifest && r.Method == http.MethodPut:
		d.putManifest(w, r, name, req.Ref)
	case req.Kind == ocipath.KindManifest && r.Method == http.MethodDelete:
		d.deleteManifest(w, name, req.Ref)
	default:
		d.unexpected = append(d.unexpected, r.Method+" "+r.URL.RequestURI())
		d.fail(w, http.StatusNotFound, "UNSUPPORTED")
	}
}

func (d *fakeDocker) headBlob(w http.ResponseWriter, name, digest string) {
	d.heads++

	if data, ok := d.image(name).blobs[digest]; ok {
		w.Header().Set(ociDigestHeader, digest)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)

		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func (d *fakeDocker) startUpload(w http.ResponseWriter, r *http.Request, name string) {
	d.oa.writes++

	q := r.URL.Query()
	img := d.image(name)

	if digest := q.Get("mount"); digest != "" {
		if _, ok := img.blobs[digest]; !ok {
			if data, ok := d.image(q.Get("from")).blobs[digest]; ok {
				img.blobs[digest] = data
				d.events = append(d.events, fmt.Sprintf("mount %s@%s from %s", name, digest, q.Get("from")))
				w.Header().Set(ociDigestHeader, digest)
				w.WriteHeader(http.StatusCreated)

				return
			}
		} else {
			w.Header().Set(ociDigestHeader, digest)
			w.WriteHeader(http.StatusCreated)

			return
		}
	}

	id := d.oa.nextID()
	d.uploads[id] = &fakeDockerUpload{name: name}

	loc := "/v2/" + name + "/blobs/uploads/" + id
	if d.foreignLocation {
		loc = "http://registry.invalid" + loc
	}

	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusAccepted)
}

func (d *fakeDocker) finishUpload(w http.ResponseWriter, r *http.Request, name, id string) {
	d.oa.writes++

	up := d.uploads[id]
	if up == nil || up.name != name {
		d.fail(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
		return
	}

	if d.failBlobPuts > 0 {
		d.failBlobPuts--
		d.fail(w, http.StatusServiceUnavailable, "UNAVAILABLE")

		return
	}

	delete(d.uploads, id)

	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		d.fail(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID")
		return
	}

	digest := r.URL.Query().Get("digest")

	data, err := io.ReadAll(r.Body)
	if err != nil || (r.ContentLength >= 0 && int64(len(data)) != r.ContentLength) {
		d.fail(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID")
		return
	}

	if got := ociDigestOf(data); got != digest {
		d.fail(w, http.StatusBadRequest, "DIGEST_INVALID")
		return
	}

	d.image(name).blobs[digest] = data
	d.blobPut[name+"@"+digest]++
	d.events = append(d.events, "blob "+name+"@"+digest)

	w.Header().Set(ociDigestHeader, digest)
	w.WriteHeader(http.StatusCreated)
}

func (d *fakeDocker) putManifest(w http.ResponseWriter, r *http.Request, name, ref string) {
	d.oa.writes++

	ctype := r.Header.Get("Content-Type")
	if !isOCIManifestType(ctype) {
		d.fail(w, http.StatusBadRequest, "MANIFEST_INVALID")
		return
	}

	body, _ := io.ReadAll(r.Body)
	isTag := !strings.Contains(ref, ":")

	if isTag && d.mangle {
		body = append(body, '\n')
	}

	digest := ociDigestOf(body)
	if !isTag && digest != ref {
		d.fail(w, http.StatusBadRequest, "DIGEST_INVALID")
		return
	}

	img := d.image(name)

	if code := d.checkReferences(img, body); code != "" {
		d.fail(w, http.StatusBadRequest, code)
		return
	}

	if old, moved := img.tags[ref]; isTag && d.immutable && moved && old != digest {
		d.fail(w, http.StatusConflict, "TAG_INVALID")
		return
	}

	img.manifests[digest] = body
	img.types[digest] = ctype

	var doc ociDocument
	_ = json.Unmarshal(body, &doc)

	if doc.Subject != nil {
		img.subjects[digest] = doc.Subject.Digest

		if !d.ignoreSubject {
			w.Header().Set(ociSubjectHeader, doc.Subject.Digest)
		}
	}

	d.events = append(d.events, "manifest "+name+" "+ref)
	d.puts = append(d.puts, fakeManifestPut{name: name, ref: ref, contentType: ctype, created: r.Header.Get(headerImportCreatedAt)})

	if isTag {
		img.tags[ref] = digest
		d.list(name, ref, digest, r.Header.Get(headerImportCreatedAt))
	}

	w.Header().Set(ociDigestHeader, digest)
	w.WriteHeader(http.StatusCreated)
}

// headManifest answers whether the image holds a manifest, by digest or tag.
func (d *fakeDocker) headManifest(w http.ResponseWriter, name, ref string) {
	d.manifestHeads++

	img := d.peek(name)
	if dg, ok := img.tags[ref]; ok {
		ref = dg
	}

	if _, ok := img.manifests[ref]; !ok {
		d.fail(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
		return
	}

	w.Header().Set(ociDigestHeader, ref)
	w.WriteHeader(http.StatusOK)
}

// referrers lists the manifests of the image whose subject is the digest, the
// way OpenArtifact's referrers endpoint does.
func (d *fakeDocker) referrers(w http.ResponseWriter, name, digest string) {
	img := d.peek(name)

	type entry struct {
		MediaType    string `json:"mediaType"` //nolint:tagliatelle // OCI wire format is camelCase
		Digest       string `json:"digest"`
		Size         int    `json:"size"`
		ArtifactType string `json:"artifactType,omitempty"` //nolint:tagliatelle // OCI wire format is camelCase
	}

	var entries []entry

	for dg, subject := range img.subjects {
		if subject != digest {
			continue
		}

		var doc struct {
			ArtifactType string `json:"artifactType"` //nolint:tagliatelle // OCI wire format is camelCase
		}

		_ = json.Unmarshal(img.manifests[dg], &doc)

		entries = append(entries, entry{MediaType: img.types[dg], Digest: dg, Size: len(img.manifests[dg]), ArtifactType: doc.ArtifactType})
	}

	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.Digest, b.Digest) })

	w.Header().Set("Content-Type", ociMediaOCIIndex)
	_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 2, "mediaType": ociMediaOCIIndex, "manifests": entries})
}

// checkReferences enforces what OpenArtifact enforces: the blobs of an image
// manifest are visible to the image, the children of an index are stored.
func (d *fakeDocker) checkReferences(img *fakeDockerImage, body []byte) string {
	var doc ociDocument
	if json.Unmarshal(body, &doc) != nil {
		return "MANIFEST_INVALID"
	}

	for _, b := range doc.payload() {
		if _, ok := img.blobs[b.Digest]; !ok {
			return "MANIFEST_BLOB_UNKNOWN"
		}
	}

	for _, m := range doc.Manifests {
		if _, ok := img.manifests[m.Digest]; !ok {
			return "MANIFEST_UNKNOWN"
		}
	}

	return ""
}

// list makes the tag visible in the package listing the importer reads.
func (d *fakeDocker) list(name, tag, digest, created string) {
	repoName, _, _ := strings.Cut(name, "/")
	repo := d.oa.repos[repoName]

	for _, it := range repo.items {
		if it.name == name && it.version == tag {
			it.path, it.sha, it.created = digest, strings.TrimPrefix(digest, ociDigestPrefix), created
			return
		}
	}

	repo.items = append(repo.items, &fakeItem{
		id: d.oa.nextID(), name: name, version: tag, path: digest,
		sha: strings.TrimPrefix(digest, ociDigestPrefix), created: created,
	})
}

func (d *fakeDocker) deleteManifest(w http.ResponseWriter, name, digest string) {
	d.oa.writes++
	d.oa.deletes++

	img := d.image(name)
	if _, ok := img.manifests[digest]; !ok {
		d.fail(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
		return
	}

	delete(img.manifests, digest)
	d.deletedManifest = append(d.deletedManifest, name+"@"+digest)

	repoName, _, _ := strings.Cut(name, "/")
	repo := d.oa.repos[repoName]

	for tag, dg := range img.tags {
		if dg == digest {
			delete(img.tags, tag)
		}
	}

	repo.items = slices.DeleteFunc(repo.items, func(it *fakeItem) bool { return it.name == name && it.path == digest })

	w.WriteHeader(http.StatusAccepted)
}

// ─── source fixture ───────────────────────────────────────────────────────────

const dockerFakeLayerType = "application/vnd.docker.image.rootfs.diff.tar.gzip"

// dockerFakeImage is an image of the fixture: a manifest, its blobs and, for an
// index, its child images.
type dockerFakeImage struct {
	digest   string
	manifest []byte
	// blobs lists the blob digests the image needs, children's included.
	blobs []string
	// manifests lists the child manifest digests.
	children []dockerFakeImage
}

type dockerFakeTag struct {
	image, tag string
	img        dockerFakeImage
}

// dockerFixture is a source registry's content.
type dockerFixture struct {
	manifests map[string][]byte
	blobs     map[string][]byte
	tags      []dockerFakeTag
	// loose are manifests stored by digest that no tag leads to.
	loose []dockerFakeTag
}

func newDockerFixture() *dockerFixture {
	return &dockerFixture{manifests: map[string][]byte{}, blobs: map[string][]byte{}}
}

func (fx *dockerFixture) blob(content string) ociDescriptor {
	b := []byte(content)
	d := ociDigestOf(b)
	fx.blobs[d] = b

	return ociDescriptor{MediaType: dockerFakeLayerType, Digest: d, Size: int64(len(b))}
}

func (fx *dockerFixture) manifestDoc(doc map[string]any) dockerFakeImage {
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}

	d := ociDigestOf(b)
	fx.manifests[d] = b

	return dockerFakeImage{digest: d, manifest: b}
}

// image builds a single-platform image with a config and layers; every string
// is the content of a blob.
func (fx *dockerFixture) image(config string, layers ...string) dockerFakeImage {
	cfg := fx.blob(config)
	cfg.MediaType = "application/vnd.docker.container.image.v1+json"

	descs := make([]ociDescriptor, 0, len(layers))
	for _, l := range layers {
		descs = append(descs, fx.blob(l))
	}

	img := fx.manifestDoc(map[string]any{
		"schemaVersion": 2, "mediaType": ociMediaDockerManifest, "config": cfg, "layers": descs,
	})
	img.blobs = append(img.blobs, cfg.Digest)

	for _, d := range descs {
		img.blobs = append(img.blobs, d.Digest)
	}

	return img
}

// imageWith builds an image whose layer descriptors are given explicitly, for
// layers the fixture should not store (foreign ones).
func (fx *dockerFixture) imageWith(config string, layers ...ociDescriptor) dockerFakeImage {
	cfg := fx.blob(config)
	cfg.MediaType = "application/vnd.docker.container.image.v1+json"

	img := fx.manifestDoc(map[string]any{
		"schemaVersion": 2, "mediaType": ociMediaDockerManifest, "config": cfg, "layers": layers,
	})
	img.blobs = []string{cfg.Digest}

	for _, l := range layers {
		if !l.foreign() {
			img.blobs = append(img.blobs, l.Digest)
		}
	}

	return img
}

// index builds a multi-platform manifest list over the children.
func (fx *dockerFixture) index(children ...dockerFakeImage) dockerFakeImage {
	var (
		entries []map[string]any
		blobs   []string
	)

	for i, c := range children {
		entries = append(entries, map[string]any{
			"mediaType": ociMediaDockerManifest, "digest": c.digest, "size": len(c.manifest),
			"platform": map[string]string{"architecture": []string{"amd64", "arm64", "riscv64"}[i%3], "os": "linux"},
		})
		blobs = append(blobs, c.blobs...)
	}

	idx := fx.manifestDoc(map[string]any{"schemaVersion": 2, "mediaType": ociMediaDockerList, "manifests": entries})
	idx.children = children
	idx.blobs = blobs

	return idx
}

func (fx *dockerFixture) tag(image, tag string, img dockerFakeImage) {
	fx.tags = append(fx.tags, dockerFakeTag{image: image, tag: tag, img: img})
}

// untagged stores the image by digest only, as a registry keeps an image whose
// tag moved on, or an artifact attached to another image.
func (fx *dockerFixture) untagged(image string, img dockerFakeImage) {
	fx.loose = append(fx.loose, dockerFakeTag{image: image, img: img})
}

const (
	dockerFakeEmptyType = "application/vnd.oci.empty.v1+json"
	dockerFakeSigType   = "application/vnd.dev.cosign.artifact.sig.v1+json"
)

// referrer builds an OCI 1.1 artifact attached to subject: an image manifest
// with the empty config, one layer of the given media type, and a subject.
func (fx *dockerFixture) referrer(subject dockerFakeImage, artifactType, payload string) dockerFakeImage {
	cfg := fx.blob("{}")
	cfg.MediaType = dockerFakeEmptyType

	layer := fx.blob(payload)
	layer.MediaType = artifactType

	var doc ociDocument
	if err := json.Unmarshal(subject.manifest, &doc); err != nil {
		panic(err)
	}

	img := fx.manifestDoc(map[string]any{
		"schemaVersion": 2, "mediaType": ociMediaOCIManifest, "artifactType": artifactType,
		"config": cfg, "layers": []ociDescriptor{layer},
		"subject":     map[string]any{"mediaType": doc.MediaType, "digest": subject.digest, "size": len(subject.manifest)},
		"annotations": map[string]string{"org.opencontainers.image.created": "2025-01-01T00:00:00Z"},
	})
	img.blobs = []string{cfg.Digest, layer.Digest}

	return img
}

// attestation builds a buildx-style attestation manifest of an image: an OCI
// manifest with an in-toto layer, tied to the image by annotations only.
func (fx *dockerFixture) attestation(of dockerFakeImage) dockerFakeImage {
	cfg := fx.blob("{}")
	cfg.MediaType = "application/vnd.oci.image.config.v1+json"

	layer := fx.blob(`{"predicateType":"https://slsa.dev/provenance/v0.2"}`)
	layer.MediaType = "application/vnd.in-toto+json"

	img := fx.manifestDoc(map[string]any{
		"schemaVersion": 2, "mediaType": ociMediaOCIManifest, "config": cfg, "layers": []ociDescriptor{layer},
		"annotations": map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": of.digest},
	})
	img.blobs = []string{cfg.Digest, layer.Digest}

	return img
}

// referrersIndex builds the index a registry without the referrers API keeps
// under the referrers tag of an image: one entry per artifact.
func (fx *dockerFixture) referrersIndex(refs ...dockerFakeImage) dockerFakeImage {
	var (
		entries []map[string]any
		blobs   []string
	)

	for _, r := range refs {
		entries = append(entries, map[string]any{"mediaType": ociMediaOCIManifest, "digest": r.digest, "size": len(r.manifest)})
		blobs = append(blobs, r.blobs...)
	}

	idx := fx.manifestDoc(map[string]any{"schemaVersion": 2, "mediaType": ociMediaOCIIndex, "manifests": entries})
	idx.children = refs
	idx.blobs = blobs

	return idx
}

// allManifests lists the digests of the image's manifest and its children's.
func (i dockerFakeImage) allManifests() []string {
	out := []string{i.digest}
	for _, c := range i.children {
		out = append(out, c.digest)
	}

	return out
}

// nexusFiles lays the fixture out as Nexus stores a hosted docker repository:
// one asset per tag, per manifest digest and per blob, and a component per tag.
func (fx *dockerFixture) nexusFiles(created string) []fakeFile {
	var (
		files []fakeFile
		seen  = map[string]bool{}
	)

	add := func(f fakeFile) {
		if !seen[f.path] {
			seen[f.path] = true
			files = append(files, f)
		}
	}

	for _, t := range fx.tags {
		base := "v2/" + t.image
		add(fakeFile{
			path: base + "/manifests/" + t.tag, data: t.img.manifest, created: created,
			contentType: ociMediaDockerManifest, compName: t.image, compVersion: t.tag,
		})

		for _, m := range t.img.allManifests() {
			add(fakeFile{path: base + "/manifests/" + m, data: fx.manifests[m], created: created, contentType: ociMediaDockerManifest})
		}

		for _, b := range t.img.blobs {
			add(fakeFile{path: base + "/blobs/" + b, data: fx.blobs[b], created: created, contentType: dockerFakeLayerType})
		}
	}

	for _, l := range fx.loose {
		base := "v2/" + l.image

		for _, m := range l.img.allManifests() {
			add(fakeFile{path: base + "/manifests/" + m, data: fx.manifests[m], created: created, contentType: fx.mediaTypeOf(m)})
		}

		for _, b := range l.img.blobs {
			add(fakeFile{path: base + "/blobs/" + b, data: fx.blobs[b], created: created, contentType: dockerFakeLayerType})
		}
	}

	return files
}

func (fx *dockerFixture) mediaTypeOf(digest string) string {
	var doc ociDocument
	_ = json.Unmarshal(fx.manifests[digest], &doc)

	return doc.MediaType
}

// artifactoryFiles lays the fixture out as Artifactory stores a local docker
// repository: a folder per tag with manifest.json (list.manifest.json for an
// index) and the blobs, folders per digest for the children, and upload junk.
func (fx *dockerFixture) artifactoryFiles(created string, props map[string]map[string][]string) []fakeFile {
	var files []fakeFile

	for _, t := range fx.tags {
		dir := t.image + "/" + t.tag + "/"

		main := "manifest.json"
		if len(t.img.children) > 0 {
			main = "list.manifest.json"
		}

		files = append(files, fakeFile{path: dir + main, data: t.img.manifest, created: created, props: props[t.image+":"+t.tag]})

		if len(t.img.children) > 0 {
			files = append(files, fakeFile{path: dir + "manifest.json", data: t.img.children[0].manifest, created: created})
		}

		for _, c := range t.img.children {
			files = append(files, fakeFile{path: t.image + "/" + strings.Replace(c.digest, ":", "__", 1) + "/manifest.json", data: c.manifest, created: created})
		}

		for _, b := range t.img.blobs {
			files = append(files, fakeFile{path: dir + strings.Replace(b, ":", "__", 1), data: fx.blobs[b], created: created})
		}
	}

	for _, l := range fx.loose {
		for _, m := range l.img.allManifests() {
			files = append(files, fakeFile{path: l.image + "/" + strings.Replace(m, ":", "__", 1) + "/manifest.json", data: fx.manifests[m], created: created})
		}
	}

	return append(files, fakeFile{path: "team/app/_uploads/junk", data: []byte("junk"), created: created})
}

// ─── source registries ────────────────────────────────────────────────────────

// dockerRegistry serves a fixture's registry API the way a real registry does.
type dockerRegistry struct {
	fx *dockerFixture

	mu     sync.Mutex
	blobs  map[string]int      // blob GETs by digest
	accept []string            // Accept headers of manifest requests
	serve  map[string][]byte   // blob content that replaces the fixture's (corruption)
	hdrs   map[string][]string // Authorization headers seen
}

func newDockerRegistry(fx *dockerFixture) *dockerRegistry {
	return &dockerRegistry{fx: fx, blobs: map[string]int{}, serve: map[string][]byte{}, hdrs: map[string][]string{}}
}

func (g *dockerRegistry) handle(w http.ResponseWriter, r *http.Request, rest string) {
	req := ocipath.Parse(rest)

	g.mu.Lock()
	defer g.mu.Unlock()

	switch req.Kind {
	case ocipath.KindManifest:
		g.accept = append(g.accept, r.Header.Get("Accept"))
		g.manifest(w, req)
	case ocipath.KindBlob:
		g.blobs[req.Ref]++

		data, ok := g.fx.blobs[req.Ref]
		if repl, bad := g.serve[req.Ref]; bad {
			data, ok = repl, true
		}

		if !ok {
			http.NotFound(w, r)
			return
		}

		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func (g *dockerRegistry) manifest(w http.ResponseWriter, req ocipath.Request) {
	var body []byte

	if strings.Contains(req.Ref, ":") {
		body = g.fx.manifests[req.Ref]
	} else {
		for _, t := range g.fx.tags {
			if t.image == req.Name && t.tag == req.Ref {
				body = t.img.manifest
			}
		}
	}

	if body == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var doc ociDocument
	_ = json.Unmarshal(body, &doc)

	sum := sha256.Sum256(body)
	w.Header().Set("Content-Type", doc.MediaType)
	w.Header().Set("Docker-Content-Digest", ociDigestPrefix+hex.EncodeToString(sum[:]))
	_, _ = w.Write(body)
}

func (g *dockerRegistry) blobGets(digest string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.blobs[digest]
}

// newDockerArtifactory serves the fixture through a fake Artifactory: the
// listing endpoints of newFakeArtifactory plus /api/docker/<repo>/v2/….
func newDockerArtifactory(t *testing.T, g *dockerRegistry, repo fakeSourceRepo) *httptest.Server {
	t.Helper()

	arti := newFakeArtifactory(t, repo)
	prefix := "/api/docker/" + repo.name + "/v2/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			arti.Config.Handler.ServeHTTP(w, r)
			return
		}

		if u, p, ok := r.BasicAuth(); !ok || u != "svc" || p != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		g.handle(w, r, rest)
	}))
	t.Cleanup(srv.Close)

	return srv
}
