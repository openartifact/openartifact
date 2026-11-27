// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pub serves hosted Dart and Flutter package repositories to "dart pub"
// and "flutter pub" through the hosted pub repository spec v2.
//
// A repository is a package server rooted at /repos/pub/{name}: the client's
// PUB_HOSTED_URL or the "hosted" url of a dependency. The package implements
// the package document (every version with its pubspec and archive checksum),
// the version document, the archive download, and "dart pub publish" (a
// three-step flow: the upload URL, the multipart archive upload, the finishing
// GET). Retraction, discontinuation, advisories and search are not served.
package pub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgpub "github.com/openartifact/openartifact/pkg/pub"
)

// FormatName is the repository format name.
const FormatName = "pub"

// metadataMaxAge is how long shared caches may reuse a public repository's
// metadata before revalidating.
const metadataMaxAge = 60

// HTTPStore is the part of the database HTTPHandler needs.
type HTTPStore interface {
	base.HandlerStore
	CreatePubPackage(ctx context.Context, c *models.PubPackage) error
	GetPubPackage(ctx context.Context, repoID string, name string, version string) (*models.PubPackage, error)
	ListPubVersions(ctx context.Context, repoID string, name string) ([]*models.PubPackage, error)
}

// HTTPHandler serves hosted pub repositories.
type HTTPHandler struct {
	*base.Handler

	DB HTTPStore
}

// NewHTTPHandler creates the handler.
func NewHTTPHandler(log *logger.Logger, db HTTPStore, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(FormatName, log, db, blobStore, baseURL), DB: db}
}

// RegisterRepoRoutes registers the pub API on the /repos/pub/{name} subrouter.
// A request for a repository of another format is answered 404.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/"+pkgpub.NewPath, h.handleNew)
	r.Post("/"+pkgpub.UploadPath, h.handleUpload)
	r.Get("/"+pkgpub.FinishPath, h.handleFinish)
	r.Get("/"+pkgpub.PackagesDir+"{package}", h.handlePackage)
	r.Get("/"+pkgpub.PackagesDir+"{package}/versions/{version}", h.handleVersion)
	r.Get("/"+pkgpub.ArchivesDir+"{file}", h.handleArchive)
}

// resolveRepo returns the pub repository named in the URL. When there is none it
// writes the response and returns nil.
func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request) *models.Repository {
	name := urlparams.Vars(r)["name"]

	repo, err := h.ResolveRepo(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return nil
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Not found.")
		return nil
	}

	return repo
}

// repoRoot is the absolute URL of the repository root below origin (the BASE_URL
// or the request's origin, see PublicBase), without a trailing slash. Every link
// the API hands out is absolute.
func repoRoot(origin, repoName string) string {
	return origin + "/repos/" + FormatName + "/" + url.PathEscape(repoName)
}

// archiveURL is the absolute download URL of an archive.
func archiveURL(origin, repoName, name, version string) string {
	return repoRoot(origin, repoName) + "/" + pkgpub.ArchivePath(name, version)
}

// errorBody is the error document of the spec.
type errorBody struct {
	Error errorItem `json:"error"`
}

type errorItem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError answers with the error document "dart pub" prints.
func writeError(w http.ResponseWriter, status int, code, message string) {
	body := []byte(`{"error":{"code":"` + code + `","message":"internal error"}}`)

	if b, err := json.Marshal(errorBody{Error: errorItem{Code: code, Message: message}}); err == nil {
		body = b
	}

	w.Header().Set("Content-Type", pkgpub.ContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
