// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package quayapi is a Quay-compatible API facade: the subset of the Quay v1
// REST API the Konflux image-controller drives to onboard a component
// (repositories, robot accounts, repository and team permissions, tags and
// notifications). OpenArtifact stands in for the Quay registry: an
// organization is a hosted docker repository, a Quay repository is an image
// path inside it, and a robot account is a service account whose deploy token
// is scoped to one or more image paths. See
// docs/integrations/konflux-image-controller.md.
//
// The facade is mounted at [Prefix] only when quayApi.enabled is set. Its
// errors are Quay-shaped ({"error_message": ...}) rather than the
// {error, code} envelope of /api/v1, because the image-controller decodes them.
package quayapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Prefix is where the facade is mounted: Konflux's quayapiurl is
// https://<host>/quay/api/v1.
const Prefix = "/quay/api/v1"

// maxBodyBytes caps a request body; every body of the Quay API is a small JSON object.
const maxBodyBytes = 1 << 20

// Authorizer is the part of the permission evaluator the facade needs.
type Authorizer interface {
	Evaluate(ctx context.Context, principal auth.Principal, repoName, artifactPath, action string) (bool, error)
}

// Handler serves the Quay-compatible API.
type Handler struct {
	db        Store
	authz     Authorizer
	log       *logger.Logger
	orgs      map[string]struct{}
	publisher webhook.Publisher
}

// New returns a Handler. organizations lists the hosted docker repositories a
// client may use as its organization; empty allows every one.
func New(db Store, authz Authorizer, log *logger.Logger, organizations []string) *Handler {
	orgs := make(map[string]struct{}, len(organizations))
	for _, o := range organizations {
		orgs[o] = struct{}{}
	}

	return &Handler{db: db, authz: authz, log: log, orgs: orgs}
}

// WithPublisher sets the webhook publisher that announces tag deletions.
func (h *Handler) WithPublisher(p webhook.Publisher) *Handler {
	h.publisher = p

	return h
}

// Register mounts the facade at [Prefix] on the authenticated router.
func (h *Handler) Register(r chi.Router) {
	r.Mount(Prefix, h)
}

// call is one request in flight.
type call struct {
	h *Handler
	w http.ResponseWriter
	r *http.Request
}

// methods maps an HTTP method to the handler of one route.
type methods map[string]func()

// ServeHTTP authorizes the request and dispatches it by path.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}

	segs := pathSegments(r.URL.Path)
	c := &call{h: h, w: w, r: r}

	var handled bool

	switch {
	case len(segs) == 1 && segs[0] == "repository":
		handled = c.repositoryRoot()
	case len(segs) >= 2 && segs[0] == "repository":
		handled = c.repositoryRoute(segs[1], segs[2:])
	case len(segs) >= 3 && segs[0] == "organization" && segs[2] == "robots":
		handled = c.robotRoute(segs[1], segs[3:])
	case len(segs) >= 4 && segs[0] == "organization" && segs[2] == "team":
		handled = c.teamRoute(segs[1], segs[3], segs[4:])
	}

	if !handled {
		writeError(w, http.StatusNotFound, "Not found")
	}
}

// authorize requires the global manage permission, the same rule the RBAC
// middleware applies to this prefix: robot accounts (deploy tokens) never pass.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	principal := middleware.PrincipalFromContext(r.Context())
	if principal == nil || principal.IsAnonymous {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return false
	}

	allowed, err := h.authz.Evaluate(r.Context(), *principal, "", "", "manage")
	if err != nil {
		h.log.Errorw("quay api: authorization failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error")

		return false
	}

	if !allowed {
		writeError(w, http.StatusForbidden, "Global manage permission required")
		return false
	}

	return true
}

// pathSegments returns the segments of the path below [Prefix]. A trailing
// slash ("tag/") is not significant.
func pathSegments(urlPath string) []string {
	rest := strings.Trim(strings.TrimPrefix(urlPath, Prefix), "/")
	if rest == "" {
		return nil
	}

	return strings.Split(rest, "/")
}

// serve runs the handler registered for the request method, 405 when none is.
func (c *call) serve(m methods) bool {
	fn, ok := m[c.r.Method]
	if !ok {
		writeError(c.w, http.StatusMethodNotAllowed, "Method not allowed")
		return true
	}

	fn()

	return true
}

var errBodyInvalid = errors.New("invalid JSON body")

// decode reads the JSON body into dst. An empty body leaves dst untouched.
func (c *call) decode(dst any) bool {
	body, err := io.ReadAll(io.LimitReader(c.r.Body, maxBodyBytes))
	if err == nil && len(strings.TrimSpace(string(body))) > 0 {
		err = json.Unmarshal(body, dst)
	}

	if err != nil {
		writeError(c.w, http.StatusBadRequest, errBodyInvalid.Error())
		return false
	}

	return true
}

// internalError logs err and answers 500.
func (c *call) internalError(msg string, err error) {
	c.h.log.Errorw("quay api: "+msg, "error", err)
	writeError(c.w, http.StatusInternalServerError, "Internal error")
}

// org resolves a Quay organization: an allowed, hosted docker repository.
// It answers 404 and returns nil otherwise.
func (c *call) org(name string) *models.Repository {
	if len(c.h.orgs) > 0 {
		if _, ok := c.h.orgs[name]; !ok {
			writeError(c.w, http.StatusNotFound, "Organization not found")
			return nil
		}
	}

	repo, err := c.h.db.GetRepositoryByName(c.r.Context(), name)
	if err != nil {
		c.internalError("look up organization", err)
		return nil
	}

	if repo == nil || repo.Format != "docker" || repo.RepoType != "hosted" {
		writeError(c.w, http.StatusNotFound, "Organization not found")
		return nil
	}

	return repo
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
}

func noContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
