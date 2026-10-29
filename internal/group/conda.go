// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgconda "github.com/openartifact/openartifact/pkg/conda"
)

const (
	// maxConcurrentCondaMerges bounds the repodata merges running at once. A
	// merge streams every member's index through one core and spools the result
	// to disk (hundreds of MiB for conda-forge), so it is far heavier than the
	// in-memory merges of the other formats.
	maxConcurrentCondaMerges = 2

	// maxCondaDocBytes bounds one member's repodata document, as served by the
	// member and decoded; the same limit the proxy applies to an upstream's.
	maxCondaDocBytes int64 = 2 << 30

	// condaMergedMaxAge is how long shared caches may reuse a public group's
	// repodata before revalidating (as hosted conda repositories).
	condaMergedMaxAge = 60

	condaJSONType = "application/json"
	condaZstdType = "application/zstd"
)

// errCondaMemberBody is a member whose repodata could not be read in full.
var errCondaMemberBody = errors.New("member repodata unreadable")

// CondaHooks is the conda group: merged repodata, package files first-hit.
var CondaHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: func(
		h *Handler,
		w http.ResponseWriter,
		r *http.Request,
		grp *models.Repository,
		relPath string,
		readable []*models.Repository,
		be Backend,
	) bool {
		return h.serveConda(w, r, grp, readable, relPath, be)
	},
	PackageRef: condaRef,
}

// serveConda answers the requests of a conda group that are not first-hit: the
// merged <subdir>/repodata.json and current_repodata.json, plain or .zst.
// Package files are first-hit (hosted wins is applied before). Everything else
// a channel may carry (channeldata.json, repodata.json.bz2, repodata.jlap,
// sharded repodata) is 404 for a group: clients fall back to what it serves.
func (h *Handler) serveConda(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	readable []*models.Repository,
	relPath string,
	be Backend,
) bool {
	p, ok := pkgconda.ParsePath(relPath)
	if ok && p.Kind == pkgconda.KindPackage {
		return false
	}

	if !ok || p.Kind != pkgconda.KindRepodata || p.Comp == pkgconda.CompBzip2 {
		http.NotFound(w, r)
		return true
	}

	h.serveCondaRepodata(w, r, grp, readable, p, be)

	return true
}

// condaMember is one member's part of a merged repodata: the member path it
// answered, and the validator it gave.
type condaMember struct {
	repo *models.Repository
	path string
	etag string
	// state is memberPresent, memberAbsent or memberFailed.
	state int
}

const (
	memberAbsent = iota
	memberPresent
	memberFailed
)

// condaMemberPaths are the paths a member may serve the repodata of p under,
// in order of preference. A proxy serves what its upstream has, the
// compressed copy being a seventh of the size; a hosted member renders its one
// repodata.json (current_repodata.json is the same document there).
func condaMemberPaths(m *models.Repository, p pkgconda.Path) []string {
	if m.RepoType != repotype.Proxy {
		return []string{p.Subdir + "/" + pkgconda.RepodataFile(false, pkgconda.CompNone)}
	}

	return []string{
		p.Subdir + "/" + pkgconda.RepodataFile(p.Current, pkgconda.CompZstd),
		p.Subdir + "/" + pkgconda.RepodataFile(p.Current, pkgconda.CompNone),
	}
}

func (h *Handler) serveCondaRepodata(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	readable []*models.Repository,
	p pkgconda.Path,
	be Backend,
) {
	hidden, ok := h.condaHiddenNames(w, grp, readable, r)
	if !ok {
		return
	}

	members := h.probeCondaMembers(r, readable, p, be)

	var present []condaMember

	partial := false

	for _, m := range members {
		switch m.state {
		case memberPresent:
			present = append(present, m)

			if m.etag == "" {
				partial = true // cannot be validated
			}
		case memberFailed:
			partial = true
		case memberAbsent:
		}
	}

	if len(present) == 0 {
		if partial {
			http.Error(w, "member repository error", http.StatusBadGateway)
		} else {
			http.NotFound(w, r)
		}

		return
	}

	contentType := condaJSONType
	if p.Comp == pkgconda.CompZstd {
		contentType = condaZstdType
	}

	h.setCondaHeaders(w, contentType)

	if revalidateMerged(w, r, grp.Public, condaETag(grp, p, present, hidden), condaMergedMaxAge, partial) {
		return
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	doc, mergeFailed := h.mergeCondaMembers(w, r, p, present, hidden, be)
	if doc == nil {
		return
	}
	defer doc.Close()

	if mergeFailed && !partial {
		// A member broke after it answered the probe: what is sent lacks its
		// packages and must not be cached under the validator already set.
		w.Header().Del("ETag")
		w.Header().Set("Cache-Control", "no-store")
	}

	h.writeCondaDocument(w, p, doc)
}

func (h *Handler) setCondaHeaders(w http.ResponseWriter, contentType string) {
	hdr := w.Header()
	hdr.Set("Content-Type", contentType)
	hdr.Set("X-Content-Type-Options", "nosniff")
}

// condaHiddenNames is the set of package names (lowercase) the hosted members
// hold: the proxy members' packages of those names are left out of the merged
// repodata (hosted wins). It is nil when nothing is hidden: the group merges
// proxy versions, or lacks a hosted or a proxy member. A hosted member whose
// names cannot be read fails the request (ok false, 502 written): serving the
// proxies then could hand out the very packages that must stay hidden.
func (h *Handler) condaHiddenNames(
	w http.ResponseWriter,
	grp *models.Repository,
	readable []*models.Repository,
	r *http.Request,
) (map[string]struct{}, bool) {
	hosted, hasProxy := splitHosted(readable)
	if grp.MergeProxyVersions || len(hosted) == 0 || !hasProxy {
		return nil, true
	}

	hidden := map[string]struct{}{}

	for _, m := range hosted {
		names, err := h.db.ListCondaPackageNames(r.Context(), m.ID)
		if err != nil {
			h.log.Errorw("failed to list hosted conda package names", "member", m.Name, "error", err)
			http.Error(w, "member repository error", http.StatusBadGateway)

			return nil, false
		}

		for _, n := range names {
			hidden[strings.ToLower(n)] = struct{}{}
		}
	}

	return hidden, true
}

// condaETag identifies one merged document: the group and its readable
// members, the validators they answered, the names hidden and the encoding. Any
// change in what the members serve or in what the group hides changes it.
func condaETag(grp *models.Repository, p pkgconda.Path, present []condaMember, hidden map[string]struct{}) string {
	h := sha256.New()

	fmt.Fprintf(h, "%s\x00%s\x00%t\x00%d", grp.ID, p.Subdir, p.Current, p.Comp)

	for _, m := range present {
		fmt.Fprintf(h, "\x00%s\x00%s\x00%s", m.repo.ID, m.path, m.etag)
	}

	// Sorted by the hash of each name: order independent without a sort.
	var sum [sha256.Size]byte

	for name := range hidden {
		s := sha256.Sum256([]byte(name))
		for i := range sum {
			sum[i] ^= s[i]
		}
	}

	fmt.Fprintf(h, "\x00%d\x00%x", len(hidden), sum)

	return `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// probeCondaMembers asks every member for the validator of its repodata
// without reading it: a GET with If-None-Match: * is answered 304 (with the
// ETag) by hosted members and by proxies.
func (h *Handler) probeCondaMembers(r *http.Request, readable []*models.Repository, p pkgconda.Path, be Backend) []condaMember {
	out := make([]condaMember, 0, len(readable))

	for _, m := range readable {
		cm := condaMember{repo: m, state: memberAbsent}

		for _, path := range condaMemberPaths(m, p) {
			mr := probeRequest(r, PkgRef{})
			mr.Header.Set("If-None-Match", "*")

			pw := &etagProbe{header: make(http.Header)}
			be.ServeMember(pw, mr, m, path)

			switch pw.status {
			case http.StatusNotFound:
				continue
			case http.StatusNotModified, http.StatusOK:
				cm.state, cm.path, cm.etag = memberPresent, path, pw.header.Get("ETag")
			default:
				cm.state = memberFailed
			}

			break
		}

		out = append(out, cm)
	}

	return out
}

// etagProbe records a member's status and headers and refuses the body: a
// member that does not answer If-None-Match: * with a 304 is not read.
type etagProbe struct {
	header http.Header
	status int
}

func (e *etagProbe) Header() http.Header { return e.header }

func (e *etagProbe) WriteHeader(code int) {
	if e.status == 0 {
		e.status = code
	}
}

func (e *etagProbe) Write(b []byte) (int, error) {
	e.WriteHeader(http.StatusOK)

	if e.status == http.StatusOK {
		return 0, io.ErrClosedPipe
	}

	return len(b), nil
}

// mergeCondaMembers merges the members' repodata, in order, into one document
// spooled to disk. It returns nil when the request was answered (the client
// went away, or no document could be built); mergeFailed reports that a member
// could not be read in full, so the document is partial.
func (h *Handler) mergeCondaMembers(
	w http.ResponseWriter,
	r *http.Request,
	p pkgconda.Path,
	members []condaMember,
	hidden map[string]struct{},
	be Backend,
) (*pkgconda.Document, bool) {
	if err := h.condaMerges.Acquire(r.Context()); err != nil {
		return nil, false
	}
	defer h.condaMerges.Release()

	b, err := pkgconda.NewBuilder(p.Subdir, "")
	if err != nil {
		h.log.Errorw("failed to start conda repodata merge", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return nil, false
	}
	defer b.Close()

	mergeFailed := false

	for _, m := range members {
		opts := pkgconda.MemberOptions{MaxBytes: maxCondaDocBytes}

		if m.repo.RepoType == repotype.Proxy && len(hidden) > 0 {
			opts.Hidden = func(name string) bool {
				_, ok := hidden[strings.ToLower(name)]
				return ok
			}
		}

		if merr := h.addCondaMember(r, b, m, opts, be); merr != nil {
			if r.Context().Err() != nil {
				return nil, false
			}

			h.log.Warnw("conda group member repodata skipped", "member", m.repo.Name, "path", m.path, "error", merr)

			mergeFailed = true
		}
	}

	doc, err := b.Finish()
	if err != nil {
		h.log.Errorw("failed to finish conda repodata merge", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return nil, false
	}

	return doc, mergeFailed
}

// addCondaMember spools one member's repodata to a temporary file and merges
// it, so neither the member's handler nor the merge holds the document in
// memory.
func (h *Handler) addCondaMember(r *http.Request, b *pkgconda.Builder, m condaMember, opts pkgconda.MemberOptions, be Backend) error {
	spool, err := os.CreateTemp("", "openartifact-conda-member-*")
	if err != nil {
		return fmt.Errorf("create spool: %w", err)
	}

	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	fw := &fileResponse{header: make(http.Header), dst: spool, limit: maxCondaDocBytes}
	be.ServeMember(fw, probeRequest(r, PkgRef{}), m.repo, m.path)

	switch {
	case fw.err != nil:
		return fmt.Errorf("%w: %w", errCondaMemberBody, fw.err)
	case fw.status != http.StatusOK && fw.status != 0:
		return fmt.Errorf("%w: status %d", errCondaMemberBody, fw.status)
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	_, err = b.Add(spool, opts)
	if err != nil {
		return fmt.Errorf("%w: %w", errCondaMemberBody, err)
	}

	return nil
}

// fileResponse is the ResponseWriter a member writes its 200 body into: a file.
type fileResponse struct {
	header http.Header
	dst    io.Writer
	limit  int64
	status int
	n      int64
	err    error
}

func (f *fileResponse) Header() http.Header { return f.header }

func (f *fileResponse) WriteHeader(code int) {
	if f.status == 0 {
		f.status = code
	}
}

func (f *fileResponse) Write(b []byte) (int, error) {
	f.WriteHeader(http.StatusOK)

	if f.status != http.StatusOK {
		return len(b), nil
	}

	if f.n += int64(len(b)); f.n > f.limit {
		f.err = pkgconda.ErrTooLarge
		return 0, f.err
	}

	n, err := f.dst.Write(b)
	if err != nil {
		f.err = err
	}

	return n, err //nolint:wrapcheck // surfaced through f.err
}

// writeCondaDocument sends the merged document: plain with its length, or
// compressed on the fly.
func (h *Handler) writeCondaDocument(w http.ResponseWriter, p pkgconda.Path, doc *pkgconda.Document) {
	if p.Comp == pkgconda.CompZstd {
		w.WriteHeader(http.StatusOK)

		enc, err := zstd.NewWriter(w, zstd.WithEncoderConcurrency(2))
		if err != nil {
			h.log.Errorw("failed to start zstd encoder", "error", err)
			return
		}

		// The status line is out: a failed copy can only abort the body.
		if _, err := doc.WriteTo(enc); err != nil {
			_ = enc.Close()
			return
		}

		_ = enc.Close()

		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(doc.Size(), 10))
	w.WriteHeader(http.StatusOK)

	_, _ = doc.WriteTo(w)
}

// ownsCondaName reports whether hosted member m holds a package of ref's
// condaName in any platform.
func (h *Handler) ownsCondaName(mr *http.Request, m *models.Repository, ref PkgRef, _ Backend) ownership {
	name := ref.condaName

	has, err := h.db.HasCondaPackageName(mr.Context(), m.ID, name)
	if err != nil {
		h.log.Errorw("failed to look up hosted conda package name", "member", m.Name, "name", name, "error", err)
		return ownsUnknown
	}

	if has {
		return ownsYes
	}

	return ownsNot
}

// condaRef is the package a conda package file request names, by the name in
// its file name.
func condaRef(relPath string) (PkgRef, bool) {
	p, ok := pkgconda.ParsePath(relPath)
	if !ok || p.Kind != pkgconda.KindPackage {
		return PkgRef{}, false
	}

	f, ok := pkgconda.ParseFilename(p.File)
	if !ok {
		return PkgRef{}, false
	}

	return PkgRef{condaName: strings.ToLower(f.Name), file: true, owner: (*Handler).ownsCondaName}, true
}
