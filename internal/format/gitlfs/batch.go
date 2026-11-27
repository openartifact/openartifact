// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gitlfs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/gitlfs"
)

// handleBatch answers POST objects/batch: for every object it names, the
// actions that move it (download, or upload plus verify) or the reason it
// cannot be moved. The actions are plain URLs of this repository; the client
// authenticates them like the batch request.
func (h *HTTPHandler) handleBatch(w http.ResponseWriter, r *http.Request) {
	repoName := urlparams.Vars(r)["name"]

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	req, ok := h.readBatch(w, r)
	if !ok {
		return
	}

	oids := make([]string, 0, len(req.Objects))

	for _, p := range req.Objects {
		if gitlfs.ValidOID(p.OID) {
			oids = append(oids, p.OID)
		}
	}

	stored, err := h.DB.GetGitLFSObjects(r.Context(), repoID, oids)
	if err != nil {
		h.Log.Errorw("failed to look up git lfs objects", "repo", repoName, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	byOID := make(map[string]*models.GitLFSObject, len(stored))
	for _, o := range stored {
		byOID[o.OID] = o
	}

	// PublicBase may add Vary: it runs before the header is written.
	objectBase := h.PublicBase(w, r) + "/repos/gitlfs/" + repoName

	resp := gitlfs.BatchResponse{Transfer: gitlfs.TransferBasic, Objects: make([]gitlfs.ObjectResult, 0, len(req.Objects))}

	for _, p := range req.Objects {
		res, err := h.batchObject(r, repoID, objectBase, req.Operation, p, byOID[p.OID])
		if err != nil {
			h.Log.Errorw("failed to check git lfs object", "repo", repoName, "oid", p.OID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")

			return
		}

		resp.Objects = append(resp.Objects, res)
	}

	writeJSON(w, http.StatusOK, resp)
}

// readBatch reads and validates the batch document; it answers the failure and
// returns false when the request cannot be served at all (per-object problems
// are answered per object).
func (h *HTTPHandler) readBatch(w http.ResponseWriter, r *http.Request) (*gitlfs.BatchRequest, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, gitlfs.MaxBatchBytes))
	if err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "batch request too large")
			return nil, false
		}

		writeError(w, http.StatusBadRequest, "failed to read the batch request")

		return nil, false
	}

	req, err := gitlfs.ParseBatch(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the batch request is not a valid JSON document")
		return nil, false
	}

	if req.Operation != gitlfs.OperationDownload && req.Operation != gitlfs.OperationUpload {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("unknown operation %q: want download or upload", req.Operation))
		return nil, false
	}

	if len(req.Transfers) > 0 && !slices.Contains(req.Transfers, gitlfs.TransferBasic) {
		writeError(w, http.StatusUnprocessableEntity, "no supported transfer: this server speaks basic")
		return nil, false
	}

	if req.HashAlgo != "" && req.HashAlgo != gitlfs.HashSHA256 {
		writeError(w, http.StatusConflict, fmt.Sprintf("unsupported hash_algo %q: objects are named by sha256", req.HashAlgo))
		return nil, false
	}

	return req, true
}

// batchObject answers one pointer of a batch given the stored object (nil when
// there is none). The error is only for a failed lookup.
func (h *HTTPHandler) batchObject(
	r *http.Request,
	repoID, objectBase, operation string,
	p gitlfs.Pointer,
	obj *models.GitLFSObject,
) (gitlfs.ObjectResult, error) {
	res := gitlfs.ObjectResult{OID: p.OID, Size: p.Size}
	fail := func(code int, msg string) (gitlfs.ObjectResult, error) {
		res.Error = &gitlfs.ObjectError{Code: code, Message: msg}
		return res, nil
	}

	switch {
	case !gitlfs.ValidOID(p.OID):
		return fail(http.StatusUnprocessableEntity, "invalid object id: want a lower-case hex sha256")
	case p.Size < 0:
		return fail(http.StatusUnprocessableEntity, "invalid object size")
	}

	if obj != nil && obj.SizeBytes != p.Size {
		return fail(http.StatusUnprocessableEntity, fmt.Sprintf("the stored object has %d bytes, not %d", obj.SizeBytes, p.Size))
	}

	if operation == gitlfs.OperationDownload {
		if obj == nil {
			return fail(http.StatusNotFound, "object not found")
		}

		blocked, err := quarantine.IsQuarantined(
			r.Context(),
			h.DB,
			repoID,
			quarantine.PathKeys(obj.ID, gitlfs.ObjectPath(obj.OID), obj.OID)...)
		if err != nil {
			return res, fmt.Errorf("check quarantine: %w", err)
		}

		if blocked {
			return fail(http.StatusForbidden, "object is quarantined")
		}

		res.Actions = map[string]gitlfs.Action{gitlfs.ActionDownload: {Href: objectBase + "/objects/" + p.OID}}

		return res, nil
	}

	// An upload of an object the repository holds has nothing to do: a result
	// without actions tells the client to skip it.
	if obj != nil {
		return res, nil
	}

	res.Actions = map[string]gitlfs.Action{
		gitlfs.ActionUpload: {Href: objectBase + "/objects/" + p.OID},
		gitlfs.ActionVerify: {Href: objectBase + "/verify"},
	}

	return res, nil
}

// handleVerify answers POST verify, which a client sends after uploading an
// object: 200 when the repository holds it with that size.
func (h *HTTPHandler) handleVerify(w http.ResponseWriter, r *http.Request) {
	repoID := h.ResolveRepoIDOrRespond(w, r, urlparams.Vars(r)["name"])
	if repoID == "" {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxVerifyBytes))
	if err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "verify request too large")
			return
		}

		writeError(w, http.StatusBadRequest, "failed to read the verify request")

		return
	}

	var p gitlfs.Pointer
	if err := json.Unmarshal(body, &p); err != nil || !gitlfs.ValidOID(p.OID) {
		writeError(w, http.StatusUnprocessableEntity, "the verify request must name an object by oid and size")
		return
	}

	obj, err := h.lookupOne(r.Context(), repoID, p.OID)
	if err != nil {
		h.Log.Errorw("failed to look up git lfs object", "oid", p.OID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	switch {
	case obj == nil:
		writeError(w, http.StatusNotFound, "object not found")
	case obj.SizeBytes != p.Size:
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("the stored object has %d bytes, not %d", obj.SizeBytes, p.Size))
	default:
		w.WriteHeader(http.StatusOK)
	}
}
