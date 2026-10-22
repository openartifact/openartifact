// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package group serves group repositories: a read-only view over an ordered
// list of hosted and proxy member repositories of the same format. A request
// is answered by the first member that has the file; Maven metadata is merged
// across all members.
package group

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/pypisimple"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Backend is what a group needs from the rest of the server to resolve a
// request against one member.
type Backend struct {
	// Authorize reports whether the caller may read relPath of member. A
	// member the caller cannot read behaves exactly like a member without the
	// file, so a public group never exposes a private member.
	Authorize func(r *http.Request, member *models.Repository, relPath string) bool

	// ServeMember serves a GET/HEAD for relPath from member into w, the way a
	// direct request to that member would (404 when it has no such file).
	ServeMember func(w http.ResponseWriter, r *http.Request, member *models.Repository, relPath string)
}

// Handler resolves requests for group repositories.
type Handler struct {
	log     *logger.Logger
	db      database.Database
	baseURL string

	// packuments bounds concurrent npm packument merges.
	packuments base.Gate
}

// NewHandler creates the group handler.
func NewHandler(log *logger.Logger, db database.Database) *Handler {
	return &Handler{log: log, db: db, packuments: base.NewGate(maxConcurrentMerges)}
}

// Serve answers a request for relPath (below the repository root) of group
// grp. Groups are read-only: anything but GET/HEAD is refused.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, grp *models.Repository, relPath string, be Backend) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "group repositories are read-only", http.StatusMethodNotAllowed)

		return
	}

	members, err := h.members(r, grp)
	if err != nil {
		h.log.Errorw("failed to load group members", "group", grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	readable := make([]*models.Repository, 0, len(members))

	for _, m := range members {
		if be.Authorize(r, m, authPath(grp.Format, m, relPath)) {
			readable = append(readable, m)
		}
	}

	if grp.Format == "maven" {
		if base, suffix, ok := mavenMetadataPath(relPath); ok {
			h.serveMavenMetadata(w, r, readable, base, suffix, be)
			return
		}
	}

	if grp.Format == "pypi" && h.servePyPI(w, r, grp, readable, relPath, be) {
		return
	}

	if grp.Format == "npm" && h.serveNPM(w, r, grp, relPath, readable, be) {
		return
	}

	if grp.Format == "helm" && h.serveHelm(w, r, grp, relPath, readable, be) {
		return
	}

	if grp.Format == "cargo" && h.serveCargo(w, r, grp, relPath, readable, be) {
		return
	}

	if grp.Format == "go" && h.serveGo(w, r, grp, relPath, readable, be) {
		return
	}

	if grp.Format == "nuget" && h.serveNuGet(w, r, grp, relPath, readable, be) {
		return
	}

	h.serveFirstHit(w, r, readable, relPath, be)
}

// authPath is the path the caller is authorized against for member m: the one a
// direct request to m would be authorized with, so a path-scoped grant on the
// member means the same thing inside and outside the group.
func authPath(format string, m *models.Repository, relPath string) string {
	if format != "pypi" {
		return relPath
	}

	if kind, project, filename := pypisimple.ParsePath(relPath); kind == pypisimple.PathFile {
		return pypiFilePath(m, project, filename)
	}

	return relPath
}

// members returns grp's members in priority order. Members that cannot be
// served from a group (nested groups, a different format) are skipped.
func (h *Handler) members(r *http.Request, grp *models.Repository) ([]*models.Repository, error) {
	links, err := h.db.ListGroupMembers(r.Context(), grp.ID)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs
	}

	out := make([]*models.Repository, 0, len(links))

	for _, l := range links {
		m, err := h.db.GetRepositoryByID(r.Context(), l.MemberRepoID)
		if err != nil {
			return nil, err //nolint:wrapcheck // caller logs
		}

		if m == nil || m.RepoType == repotype.Group || m.Format != grp.Format || m.Blocked {
			continue
		}

		out = append(out, m)
	}

	return out, nil
}

// serveFirstHit tries each member in order behind a probeWriter. A member that
// answers 404 — or fails with a 5xx — is passed over; the first other answer
// is streamed straight to the client. When nobody has the file the group
// answers 404, or 502 if a member failed (so a client does not cache "does not
// exist" for a file a broken member might have).
func (h *Handler) serveFirstHit(w http.ResponseWriter, r *http.Request, members []*models.Repository, relPath string, be Backend) {
	h.serveFirstHitAs(w, r, members, func(*models.Repository) string { return relPath }, be)
}

// serveFirstHitAs is serveFirstHit for formats whose member paths differ by
// member type: pathFor returns the path to ask each member for.
func (h *Handler) serveFirstHitAs(w http.ResponseWriter, r *http.Request, members []*models.Repository, pathFor func(*models.Repository) string, be Backend) {
	memberFailed := false

	for _, m := range members {
		pw := newProbeWriter(w)
		be.ServeMember(pw, r, m, pathFor(m))

		switch {
		case pw.committed:
			return
		case pw.failed:
			memberFailed = true
		}
	}

	if memberFailed {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	http.NotFound(w, r)
}

// probeWriter wraps the real ResponseWriter for one member attempt. It
// withholds the status and headers until WriteHeader: a 404 or 5xx marks the
// attempt as passed over and discards everything written after, so the caller
// can try the next member; any other status commits at once and streams the
// body straight through, never buffering it.
type probeWriter struct {
	real        http.ResponseWriter
	header      http.Header
	wroteHeader bool
	committed   bool
	failed      bool
}

func newProbeWriter(w http.ResponseWriter) *probeWriter {
	return &probeWriter{real: w, header: make(http.Header)}
}

func (p *probeWriter) Header() http.Header {
	if p.committed {
		return p.real.Header()
	}

	return p.header
}

func (p *probeWriter) WriteHeader(code int) {
	if p.wroteHeader {
		return
	}

	p.wroteHeader = true

	if code == http.StatusNotFound || code >= http.StatusInternalServerError {
		p.failed = code != http.StatusNotFound

		return
	}

	dst := p.real.Header()
	for k, vals := range p.header {
		for _, v := range vals {
			dst.Add(k, v)
		}
	}

	// Group members stream stored artifacts verbatim; never let a browser
	// sniff them into HTML/script, and default to a download type.
	if dst.Get("Content-Type") == "" {
		dst.Set("Content-Type", "application/octet-stream")
	}

	dst.Set("X-Content-Type-Options", "nosniff")

	p.real.WriteHeader(code)
	p.committed = true
}

func (p *probeWriter) Write(b []byte) (int, error) {
	if !p.wroteHeader {
		p.WriteHeader(http.StatusOK)
	}

	if !p.committed {
		return len(b), nil
	}

	return p.real.Write(b)
}

// Flush forwards to the real writer once the attempt has committed.
func (p *probeWriter) Flush() {
	if f, ok := p.real.(http.Flusher); ok && p.committed {
		f.Flush()
	}
}

// revalidateMerged is base.Revalidate for a document merged from members. A
// document assembled while a member was failing (partial) lacks what that
// member holds, so it must not be cached or revalidated into place: it is sent
// with Cache-Control: no-store and no validator. It returns true when a 304
// was written.
func revalidateMerged(w http.ResponseWriter, r *http.Request, public bool, etag string, maxAge int, partial bool) bool {
	if partial {
		w.Header().Set("Cache-Control", "no-store")
		return false
	}

	return base.Revalidate(w, r, public, etag, maxAge)
}
