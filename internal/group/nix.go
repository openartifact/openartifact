// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	formatnix "github.com/openartifact/openartifact/internal/format/nix"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/pkg/nix"
)

// WithNixKeys enables the signing of nix groups: keys resolves the key of the
// group itself (*nixkeys.Service), the one it signs the narinfo of its hosted
// members with and publishes at /public.key.
func (h *Handler) WithNixKeys(keys formatnix.KeyProvider) *Handler {
	h.nixKeys = keys
	return h
}

// NixHooks is the nix group: answers the cache documents and narinfo itself,
// the first member that has a package answers. No hosted-wins: hosted members
// are asked before proxy ones and there are no package names to hide.
var NixHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: func(
		h *Handler,
		w http.ResponseWriter,
		r *http.Request,
		grp *models.Repository,
		relPath string,
		readable []*models.Repository,
		be Backend,
	) bool {
		h.serveNix(w, r, grp, readable, relPath, be)

		return true
	},
}

// serveNix answers a request of a nix group, a binary cache over its members:
// nix-cache-info and public.key are the group's own, a narinfo comes from the
// first member that has it and everything else (NARs, listings) from the
// first member that has the file. Hosted members are asked before proxy
// members, so what a private cache holds is never answered from an upstream.
// readable are the members the caller may read.
//
// A narinfo of a hosted member gains the group's signature (the member's own
// stays): a client that trusts the group's public key accepts everything the
// group vouches for. A narinfo of a proxy member is served as the upstream
// wrote it: the group did not vouch for it, so the client needs the upstream's
// key for it.
func (h *Handler) serveNix(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	readable []*models.Repository,
	relPath string,
	be Backend,
) {
	// Everything a group serves is about its members; a caller who can read
	// none of them has nothing to ask for.
	if len(readable) == 0 {
		http.NotFound(w, r)
		return
	}

	rel := strings.Trim(relPath, "/")

	switch rel {
	case "nix-cache-info":
		formatnix.ServeCacheInfo(w, r)
		return
	case "public.key":
		formatnix.ServePublicKey(w, r, h.log, h.nixKeys, grp)

		return
	}

	hosted, proxies := splitNixMembers(readable)

	if hash, ok := strings.CutSuffix(rel, ".narinfo"); ok && nix.ValidStoreHash(hash) {
		h.serveNixNarInfo(w, r, grp, hosted, proxies, relPath, be)
		return
	}

	h.serveFirstHit(w, r, slices.Concat(hosted, proxies), relPath, be)
}

// splitNixMembers separates the hosted members from the proxy ones, each in
// the group's order.
func splitNixMembers(readable []*models.Repository) ([]*models.Repository, []*models.Repository) {
	var hosted, proxies []*models.Repository

	for _, m := range readable {
		if m.RepoType == repotype.Proxy {
			proxies = append(proxies, m)
		} else {
			hosted = append(hosted, m)
		}
	}

	return hosted, proxies
}

// serveNixNarInfo answers a narinfo request. A hosted member's answer is
// captured to be signed; a proxy member's is streamed as it is. A member that
// has no such narinfo or fails is passed over (a failure makes the final
// answer a 502 instead of a 404: the narinfo may exist behind it).
func (h *Handler) serveNixNarInfo(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	hosted, proxies []*models.Repository,
	relPath string,
	be Backend,
) {
	// Hosted members are asked for the narinfo itself, always with GET and
	// without the client's conditional headers: the group answers HEAD and
	// revalidation from the document it signed.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.Header.Del("If-None-Match")
	mr.Header.Del("If-Modified-Since")

	failed := false
	sess := h.newSession(r.Context())

	defer sess.release()

	for _, m := range hosted {
		cw := sess.capture(nix.MaxNarInfoBytes)
		be.ServeMember(cw, mr, m, relPath)

		switch {
		case cw.status == http.StatusNotFound:
		case cw.overflow || cw.status == 0 || cw.status >= http.StatusInternalServerError:
			failed = true
		case cw.status == http.StatusOK:
			sess.release()
			h.writeSignedNarInfo(w, r, grp, cw.body.Bytes())

			return
		default:
			// Neither found nor failed (a quarantined path, say): the member's
			// answer is the group's.
			sess.release()
			replayCapture(w, r, cw)

			return
		}
	}

	sess.release()

	served, memberFailed := firstHit(w, r, proxies, func(*models.Repository) string { return relPath }, be)

	switch {
	case served:
	case failed || memberFailed:
		http.Error(w, "member repository error", http.StatusBadGateway)
	default:
		http.NotFound(w, r)
	}
}

// writeSignedNarInfo answers with a hosted member's narinfo plus the group's
// signature. A group without a key (no OPENARTIFACT_SECRET_KEY) serves it as
// the member rendered it.
func (h *Handler) writeSignedNarInfo(w http.ResponseWriter, r *http.Request, grp *models.Repository, raw []byte) {
	key, err := formatnix.SigningKey(r.Context(), h.nixKeys, grp)
	if err != nil {
		h.log.Errorw("failed to load nix signing key", "group", grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	body := string(raw)

	if key != nil {
		n, err := nix.ParseNarInfo(raw)
		if err == nil {
			err = formatnix.SignNarInfo(n, key)
		}

		if err != nil {
			h.log.Errorw("failed to sign a member's narinfo", "group", grp.Name, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		body = n.String()
	}

	formatnix.WriteNarInfo(w, r, grp.Public, body)
}

// replayCapture sends a captured member answer to the client unchanged.
func replayCapture(w http.ResponseWriter, r *http.Request, cw *captureWriter) {
	dst := w.Header()

	for k, vals := range cw.header {
		for _, v := range vals {
			dst.Add(k, v)
		}
	}

	dst.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(cw.status)

	if r.Method != http.MethodHead {
		_, _ = w.Write(cw.body.Bytes())
	}
}
