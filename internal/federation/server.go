// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	defaultLogLimit = 500
	maxLogLimit     = 2000
	maxKeyLength    = 4096
)

// Error codes of the peer endpoints.
const (
	codeUnauthorized = "FEDERATION_UNAUTHORIZED"
	codeNotFound     = "FEDERATION_NOT_FOUND"
	codeInvalid      = "FEDERATION_INVALID_REQUEST"
	codeQuarantined  = "ARTIFACT_QUARANTINED"
	codeInternal     = "INTERNAL_ERROR"
)

// Server answers the pull requests of peers under /federation/v1. It is
// mounted outside the RBAC chain: peers authenticate with a per-repository
// bearer token (see AuthenticateToken), which grants read access to the change
// log and the content of that one repository and nothing else.
type Server struct {
	store *Store
	db    database.Database
	blobs storage.BlobStore
	log   *logger.Logger
}

// NewServer creates the peer endpoint handler.
func NewServer(store *Store, db database.Database, blobs storage.BlobStore, log *logger.Logger) *Server {
	return &Server{store: store, db: db, blobs: blobs, log: log}
}

type ctxKey struct{}

// Handler returns the router of the peer endpoints, to be mounted at /federation/v1.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.authenticate)
	r.Get("/info", s.info)
	r.Get("/log", s.logPage)
	r.Get("/unit", s.unit)
	r.Get("/blob", s.blob)

	return r
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")

	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}

	return ""
}

// authenticate resolves the bearer token to its repository and refuses a
// repository that is not federated. Every refusal is the same 401, so a probe
// learns nothing about which tokens or repositories exist.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		token := bearer(r)
		if token == "" {
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized, "peer token required")

			return
		}

		repoID, err := s.store.AuthenticateToken(r.Context(), token)
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized, "peer token refused")

			return
		}

		if err != nil {
			s.log.Errorw("federation: authenticate peer token", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, codeInternal, "internal error")

			return
		}

		repo, err := s.store.RepoByID(r.Context(), repoID)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized, "peer token refused")

			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, repo)))
	})
}

func repoFrom(r *http.Request) *RepoInfo {
	repo, _ := r.Context().Value(ctxKey{}).(*RepoInfo)

	return repo
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	repo := repoFrom(r)

	st, err := s.store.RepoState(r.Context(), repo.ID)
	if errors.Is(err, ErrNotFederated) {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "repository is not federated")

		return
	}

	if err != nil {
		s.fail(w, "read federation state", err)

		return
	}

	instance, err := s.store.InstanceID(r.Context())
	if err != nil {
		s.fail(w, "read instance id", err)

		return
	}

	httpx.WriteJSON(w, http.StatusOK, InfoResponse{
		Instance: instance, LogID: st.LogID, HeadSeq: st.HeadSeq,
		Repository: repo.Name, Format: repo.Format, Version: ProtocolVersion,
	})
}

func (s *Server) logPage(w http.ResponseWriter, r *http.Request) {
	repo := repoFrom(r)
	q := r.URL.Query()

	after, err := strconv.ParseInt(q.Get("after"), 10, 64)
	if q.Get("after") == "" {
		after, err = 0, nil
	}

	if err != nil || after < 0 {
		httpx.WriteError(w, http.StatusBadRequest, codeInvalid, "after must be a non-negative integer")

		return
	}

	limit := defaultLogLimit

	if v := q.Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 {
			httpx.WriteError(w, http.StatusBadRequest, codeInvalid, "limit must be a positive integer")

			return
		}
	}

	limit = min(limit, maxLogLimit)

	st, err := s.store.RepoState(r.Context(), repo.ID)
	if errors.Is(err, ErrNotFederated) {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "repository is not federated")

		return
	}

	if err != nil {
		s.fail(w, "read federation state", err)

		return
	}

	entries, err := s.store.ListEntries(r.Context(), repo.ID, after, limit+1)
	if err != nil {
		s.fail(w, "read federation log", err)

		return
	}

	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}

	if entries == nil {
		entries = []Entry{}
	}

	httpx.WriteJSON(w, http.StatusOK, LogResponse{LogID: st.LogID, HeadSeq: st.HeadSeq, Entries: entries, More: more})
}

// unitKey reads and bounds the unit key of a request.
func unitKey(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	key := r.URL.Query().Get(param)
	if key == "" || len(key) > maxKeyLength {
		httpx.WriteError(w, http.StatusBadRequest, codeInvalid, param+" is required")

		return "", false
	}

	return key, true
}

// unit serves the current state of one unit. The log row is read before the
// content: a change landing in between is then served under the older stamp,
// which the receiver corrects when the newer log entry arrives (the reverse
// order could attach newer stamp to older content and lose the change).
func (s *Server) unit(w http.ResponseWriter, r *http.Request) {
	repo := repoFrom(r)

	key, ok := unitKey(w, r, "key")
	if !ok {
		return
	}

	entry, err := s.store.GetEntry(r.Context(), s.store.db, repo.ID, key)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "unit not found")

		return
	}

	if err != nil {
		s.fail(w, "read federation unit", err)

		return
	}

	if entry.Tombstone {
		httpx.WriteJSON(w, http.StatusOK, UnitResponse{Entry: *entry})

		return
	}

	content, ok := s.export(w, r, repo, key)
	if !ok {
		return
	}

	httpx.WriteJSON(w, http.StatusOK, UnitResponse{Entry: *entry, Content: content})
}

// export reads a live unit and refuses a quarantined one; it writes the error
// response itself and reports false in that case.
func (s *Server) export(w http.ResponseWriter, r *http.Request, repo *RepoInfo, key string) (*Content, bool) {
	codec, err := CodecFor(repo.Format)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "format is not federated")

		return nil, false
	}

	blocked, err := quarantine.IsQuarantined(r.Context(), s.db, repo.ID, quarantineKeys(repo.Format, key)...)
	if err != nil {
		s.fail(w, "check quarantine", err)

		return nil, false
	}

	if blocked {
		httpx.WriteError(w, http.StatusLocked, codeQuarantined, "unit is quarantined")

		return nil, false
	}

	content, err := codec.Export(r.Context(), s.store.db, repo.ID, key)
	if err != nil {
		s.fail(w, "export federation unit", err)

		return nil, false
	}

	if content == nil {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "unit has no live state")

		return nil, false
	}

	return content, true
}

// blob streams one blob of a unit. The unit is exported first and the blob
// must be one of its own, so a token never reads a blob outside its repository.
func (s *Server) blob(w http.ResponseWriter, r *http.Request) {
	repo := repoFrom(r)

	key, ok := unitKey(w, r, "unit")
	if !ok {
		return
	}

	sha := r.URL.Query().Get("sha256")
	if storage.ValidateBlobID(sha) != nil {
		httpx.WriteError(w, http.StatusBadRequest, codeInvalid, "sha256 is required")

		return
	}

	content, ok := s.export(w, r, repo, key)
	if !ok {
		return
	}

	var ref *BlobRef

	for i := range content.Blobs {
		if subtle.ConstantTimeCompare([]byte(content.Blobs[i].SHA256), []byte(sha)) == 1 {
			ref = &content.Blobs[i]

			break
		}
	}

	if ref == nil {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "blob is not part of the unit")

		return
	}

	s.serveBlob(w, r, ref)
}

// serveBlob streams the bytes of ref, honouring a single Range request so a
// peer can resume an interrupted pull. The caller has already established
// that ref belongs to the authenticated unit.
func (s *Server) serveBlob(w http.ResponseWriter, r *http.Request, ref *BlobRef) {
	span, err := parseRange(r.Header.Get("Range"), ref.Size)
	if err != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(ref.Size, 10))
		httpx.WriteError(w, http.StatusRequestedRangeNotSatisfiable, codeInvalid, "range not satisfiable")

		return
	}

	// ref.SHA256 is the unit's own blob id from the database (equal to the
	// validated request value), never the request string itself.
	body, err := storage.OpenAt(r.Context(), s.blobs, ref.SHA256, span.start)
	if errors.Is(err, storage.ErrBlobNotFound) {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "blob bytes are missing at the origin")

		return
	}

	if errors.Is(err, storage.ErrOffsetOutOfRange) {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(ref.Size, 10))
		httpx.WriteError(w, http.StatusRequestedRangeNotSatisfiable, codeInvalid, "range not satisfiable")

		return
	}

	if err != nil {
		s.fail(w, "open blob", err)

		return
	}
	defer body.Close()

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Accept-Ranges", "bytes")

	status := http.StatusOK

	switch {
	case span.partial:
		status = http.StatusPartialContent

		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", span.start, span.end, ref.Size))
		h.Set("Content-Length", strconv.FormatInt(span.end-span.start+1, 10))
	case ref.Size > 0:
		h.Set("Content-Length", strconv.FormatInt(ref.Size, 10))
	}

	w.WriteHeader(status)

	var src io.Reader = body
	if span.partial {
		src = io.LimitReader(body, span.end-span.start+1)
	}

	_, _ = iocopy.Copy(w, src) // a peer that hangs up mid-transfer retries
}

func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.log.Errorw("federation: "+what, "error", err)
	httpx.WriteError(w, http.StatusInternalServerError, codeInternal, "internal error")
}

// quarantineKeys lists the artifact paths under which a scan of the unit may
// have been recorded: the spellings the format's own download handler checks.
func quarantineKeys(format, key string) []string {
	switch format {
	case "pypi": // name/version/filename
		return quarantine.PathKeys(key, key[strings.LastIndex(key, "/")+1:])
	case "helm": // name@version
		name, ver := cutLast(key)
		file := name + "-" + ver + ".tgz"

		return quarantine.PathKeys(key, file, "charts/"+file)
	case "cargo": // name@version
		name, ver := cutLast(key)

		return quarantine.PathKeys(key, name+"-"+ver+".crate", name+"/"+ver)
	case "go": // module@version
		mod, ver := cutLast(key)
		esc := url.PathEscape(mod)

		return quarantine.PathKeys(key, mod+"/@v/"+ver+".zip", mod+"/@v/"+ver+".mod", esc+"@"+ver)
	case "nuget": // id@version
		id, ver := cutLast(key)
		file := strings.ToLower(id) + "." + strings.ToLower(ver) + ".nupkg"

		return quarantine.PathKeys(key, file)
	case "docker": // m:image@digest | t:image:tag
		return quarantine.PathKeys(dockerQuarantineKeys(key)...)
	}

	return quarantine.PathKeys(key) // raw, maven, npm: the unit key is the path
}

// cutLast splits s around the last '@'; without one the whole string is the head.
func cutLast(s string) (string, string) {
	head, tail, ok := strings.CutLast(s, "@")
	if !ok {
		return s, ""
	}

	return head, tail
}

func dockerQuarantineKeys(key string) []string {
	rest, ok := strings.CutPrefix(key, "m:")
	if !ok {
		return []string{key}
	}

	image, digest := cutLast(rest)

	return []string{key, digest, image + "/manifests/" + digest}
}
