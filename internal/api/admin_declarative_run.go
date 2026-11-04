// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/declarative"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// declarativeMaxBody bounds a diff/apply request: one manifest of at most
	// 8 MiB plus the JSON envelope and the supplied secret values.
	declarativeMaxBody = 9 << 20
	// declarativePrepareTimeout bounds reading the live state and planning.
	declarativePrepareTimeout = 2 * time.Minute
	// declarativeApplyTimeout bounds one apply; it runs detached from the
	// request so a closed browser tab cannot stop it half way.
	declarativeApplyTimeout = 10 * time.Minute
)

var errServerFileSecret = errors.New("a file secretRef cannot be read by the server; use an env secretRef and supply its value")

// declarativeRunRequest is the body of the diff and apply endpoints.
type declarativeRunRequest struct {
	// Manifest is the YAML stream (documents separated by ---), as `oa apply -f -` reads it.
	Manifest string `json:"manifest"`
	// Kinds limits the run to these kinds (case-insensitive), like `--kind`.
	Kinds    []string `json:"kinds"`
	Prune    bool     `json:"prune"`
	PruneAll bool     `json:"prune_all"`
	// ForceSecrets sends every secret even when the server reports it as set.
	ForceSecrets bool `json:"force_secrets"`
	// ContinueOnError keeps applying after a failure (apply only).
	ContinueOnError bool `json:"continue_on_error"`
	// Secrets maps the name of an env secretRef to its value. The server
	// never reads its own environment or files for a manifest.
	Secrets map[string]string `json:"secrets"`
}

// declarativeSecret is a secretRef the plan is going to send.
type declarativeSecret struct {
	Env      string `json:"env,omitempty"`
	File     string `json:"file,omitempty"`
	Provided bool   `json:"provided"`
}

// declarativeProblem is one manifest problem, positioned like `oa` prints it.
type declarativeProblem struct {
	Line    int    `json:"line,omitempty"`
	Object  string `json:"object,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// declarativeRunResponse is the plan (and, for apply, its outcome). Error and
// Code are set when the answer is a refusal that still carries the plan.
type declarativeRunResponse struct {
	*declarative.JSONReport

	Error    string               `json:"error,omitempty"`
	Code     string               `json:"code,omitempty"`
	Secrets  []declarativeSecret  `json:"secrets"`
	Problems []declarativeProblem `json:"problems,omitempty"`
}

// AdminDeclarativeRunHandler runs the `oa apply` engine for the web UI: diff,
// apply and export of declarative manifests.
//
// The engine talks to the REST API like `oa` does. Here it talks to the
// server's own router in process, carrying the caller's credentials, so every
// change goes through the same authorization, CSRF, validation and audit path
// as the same request from a client; the engine gets no extra privilege.
type AdminDeclarativeRunHandler struct {
	backend   http.Handler
	log       *logger.Logger
	evaluator *auth.Evaluator
	applying  sync.Mutex
}

// NewAdminDeclarativeRunHandler creates the handler; backend is the router
// that serves /api/v1 (the engine's requests are dispatched to it).
func NewAdminDeclarativeRunHandler(backend http.Handler, log *logger.Logger, evaluator *auth.Evaluator) *AdminDeclarativeRunHandler {
	return &AdminDeclarativeRunHandler{backend: backend, log: log, evaluator: evaluator}
}

// Register mounts the routes.
func (h *AdminDeclarativeRunHandler) Register(r chi.Router) {
	r.Post("/api/v1/admin/declarative/diff", h.handleDiff)
	r.Post("/api/v1/admin/declarative/apply", h.handleApply)
	r.Get("/api/v1/admin/declarative/export", h.handleExport)
}

func (h *AdminDeclarativeRunHandler) client(r *http.Request) *declarative.Client {
	return declarative.NewClient(
		"http://in-process.invalid",
		"",
		&http.Client{Transport: inProcessTransport{backend: h.backend, origin: r}},
	)
}

func readDeclarativeRequest(r *http.Request) (*declarativeRunRequest, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, declarativeMaxBody+1))
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller answers 400
	}

	if len(data) > declarativeMaxBody {
		return nil, errBodyTooLarge
	}

	var req declarativeRunRequest

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		return nil, err //nolint:wrapcheck // the caller answers 400
	}

	return &req, nil
}

// secretResolver resolves env refs from the request only: a manifest sent to
// the server must not be able to read the server's environment or files.
func (req *declarativeRunRequest) secretResolver() declarative.SecretResolver {
	return declarative.SecretResolver{
		LookupEnv: func(name string) (string, bool) {
			v, ok := req.Secrets[name]
			return v, ok
		},
		ReadFile: func(string) ([]byte, error) { return nil, errServerFileSecret },
	}
}

func (req *declarativeRunRequest) options(c *declarative.Client, reg *declarative.Registry) declarative.Options {
	return declarative.Options{
		Registry: reg,
		Client:   c,
		Load:     declarative.LoadOptions{Paths: []string{"-"}, Stdin: strings.NewReader(req.Manifest)},
		Kinds:    req.Kinds, Prune: req.Prune, PruneAll: req.PruneAll, ForceSecrets: req.ForceSecrets,
		ContinueOnError: req.ContinueOnError, Resolver: req.secretResolver(),
	}
}

// prepare reads and validates the request, then loads, validates and plans the
// manifests. It answers the request itself and returns nil on any failure.
func (h *AdminDeclarativeRunHandler) prepare(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
) (*declarativeRunRequest, declarative.Options, *declarative.Prepared) {
	var zero declarative.Options

	if !requireAdmin(w, r, h.evaluator) {
		return nil, zero, nil
	}

	req, err := readDeclarativeRequest(r)
	if err != nil {
		writeBadBody(w, err, "bad_request", "invalid JSON body")
		return nil, zero, nil
	}

	if strings.TrimSpace(req.Manifest) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "manifest is required")
		return nil, zero, nil
	}

	opts := req.options(h.client(r), declarative.DefaultRegistry())

	prepared, err := declarative.Prepare(ctx, opts)
	if err != nil {
		h.writePrepareError(w, err)
		return nil, zero, nil
	}

	return req, opts, prepared
}

func (h *AdminDeclarativeRunHandler) writePrepareError(w http.ResponseWriter, err error) {
	if verrs, ok := errors.AsType[declarative.ValidationErrors](err); ok {
		problems := make([]declarativeProblem, 0, len(verrs))
		for _, e := range verrs {
			problems = append(problems, problemOf(e))
		}

		writeJSON(w, http.StatusUnprocessableEntity, declarativeRunResponse{
			Error: "the manifest has problems",
			Code:  "manifest_invalid",
			// No plan exists, but the answer keeps the shape of one.
			JSONReport: declarative.BuildJSON(&declarative.Plan{}, nil),
			Secrets:    []declarativeSecret{},
			Problems:   problems,
		})

		return
	}

	if errors.Is(err, declarative.ErrUnknownKind) {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "timeout", "reading the server state took too long")
		return
	}

	// A manifest with no document is the author's mistake, not the server's.
	if strings.HasPrefix(err.Error(), "no manifest documents") {
		writeError(w, http.StatusUnprocessableEntity, "manifest_invalid", err.Error())
		return
	}

	h.log.Warnw("declarative: planning failed", "error", err)
	writeError(w, http.StatusBadGateway, "plan_failed", "planning the manifest failed; see the server log")
}

func problemOf(err error) declarativeProblem {
	if ve, ok := errors.AsType[*declarative.ValidationError](err); ok {
		return declarativeProblem{Line: ve.Pos.Line, Object: ve.Ident, Field: ve.Path, Message: ve.Msg}
	}

	return declarativeProblem{Message: err.Error()}
}

func planSecrets(plan *declarative.Plan, req *declarativeRunRequest) []declarativeSecret {
	refs := declarative.PlanSecretRefs(plan)
	out := make([]declarativeSecret, 0, len(refs))

	for _, ref := range refs {
		s := declarativeSecret{Env: ref.Env, File: ref.File}
		if ref.Env != "" {
			s.Provided = req.Secrets[ref.Env] != ""
		}

		out = append(out, s)
	}

	return out
}

func (h *AdminDeclarativeRunHandler) handleDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), declarativePrepareTimeout)
	defer cancel()

	req, _, p := h.prepare(ctx, w, r)
	if p == nil {
		return
	}

	writeJSON(w, http.StatusOK, declarativeRunResponse{JSONReport: declarative.BuildJSON(p.Plan, nil), Secrets: planSecrets(p.Plan, req)})
}

func (h *AdminDeclarativeRunHandler) handleApply(w http.ResponseWriter, r *http.Request) {
	if !h.applying.TryLock() {
		writeError(w, http.StatusConflict, "apply_in_progress", "another apply is running; wait for it to finish")
		return
	}
	defer h.applying.Unlock()

	// Detached from the request: closing the tab must not leave half a plan applied.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), declarativeApplyTimeout)
	defer cancel()

	req, opts, p := h.prepare(ctx, w, r)
	if p == nil {
		return
	}

	secrets := planSecrets(p.Plan, req)

	if errs := p.Plan.Errors(); len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, declarativeRunResponse{
			Error: "the plan cannot be applied; nothing was changed", Code: "plan_invalid",
			JSONReport: declarative.BuildJSON(p.Plan, nil), Secrets: secrets,
		})

		return
	}

	if errs := declarative.CheckSecrets(p.Plan, opts.Resolver); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}

		writeJSON(w, http.StatusUnprocessableEntity, declarativeRunResponse{
			Error: "a secret is missing; nothing was changed: " + strings.Join(msgs, "; "), Code: "secrets_missing",
			JSONReport: declarative.BuildJSON(p.Plan, nil), Secrets: secrets,
		})

		return
	}

	outcomes := declarative.Execute(ctx, opts, p)

	h.log.Infow("declarative: applied from the web UI", "items", len(p.Plan.Items), "failed", len(declarative.Failed(outcomes)))
	writeJSON(w, http.StatusOK, declarativeRunResponse{JSONReport: declarative.BuildJSON(p.Plan, outcomes), Secrets: secrets})
}

func (h *AdminDeclarativeRunHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), declarativePrepareTimeout)
	defer cancel()

	q := r.URL.Query()

	docs, err := declarative.Export(ctx, declarative.ExportOptions{
		Registry: declarative.DefaultRegistry(), Client: h.client(r), Kinds: q["kind"], WithMembers: q.Get("with_members") == "true",
	})
	if err != nil {
		if errors.Is(err, declarative.ErrUnknownKind) {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		h.log.Warnw("declarative: export failed", "error", err)
		writeError(w, http.StatusBadGateway, "export_failed", "exporting the server state failed; see the server log")

		return
	}

	var buf bytes.Buffer
	if err := declarative.WriteStream(&buf, docs); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to render the manifests")
		return
	}

	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="openartifact-manifests.yaml"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// inProcessTransport serves the engine's requests with the server's own router
// instead of the network. The caller's headers (session cookie, bearer token,
// CSRF token, forwarding headers) ride along, so the nested request is
// authenticated and authorized exactly as the caller would be.
type inProcessTransport struct {
	backend http.Handler
	origin  *http.Request
}

// hopHeaders are the origin headers that describe the original request body
// or the connection, not the caller.
var hopHeaders = []string{ //nolint:gochecknoglobals // immutable list
	"Content-Length", "Content-Type", "Content-Encoding", "Accept", "Accept-Encoding", "Transfer-Encoding", "Connection",
	"If-None-Match", "If-Modified-Since", "Range", "Expect",
}

func (t inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	hdr := t.origin.Header.Clone()
	for _, k := range hopHeaders {
		hdr.Del(k)
	}

	maps.Copy(hdr, req.Header)

	body := req.Body
	if body == nil {
		body = http.NoBody
	}

	sreq := (&http.Request{
		Method:     req.Method,
		URL:        &url.URL{Path: req.URL.Path, RawPath: req.URL.RawPath, RawQuery: req.URL.RawQuery},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     hdr,
		Body:       body,
		Host:       t.origin.Host,
		RemoteAddr: t.origin.RemoteAddr,
		TLS:        t.origin.TLS,
		RequestURI: req.URL.RequestURI(),

		ContentLength: req.ContentLength,
	}).WithContext(withoutRouteContext(req.Context()))

	rec := &bufferedResponse{header: http.Header{}}
	t.backend.ServeHTTP(rec, sreq)

	if rec.status == 0 {
		rec.status = http.StatusOK
	}

	return &http.Response{
		Status:        http.StatusText(rec.status),
		StatusCode:    rec.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rec.header,
		Body:          io.NopCloser(&rec.body),
		ContentLength: int64(rec.body.Len()),
		Request:       req,
	}, nil
}

// withoutRouteContext drops the chi route context of the request being served.
// A chi router that finds one in a request's context takes the request for a
// sub-route of the one in progress and dispatches it by the outer route, so the
// nested request would reach the handler that made it instead of its own route.
func withoutRouteContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
}

// bufferedResponse is an http.ResponseWriter that keeps the answer in memory.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}

	return b.body.Write(p) //nolint:wrapcheck // a bytes.Buffer write never fails
}
