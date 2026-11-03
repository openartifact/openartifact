// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"go.yaml.in/yaml/v3"
)

// openapiSpecGz is openapi.json, the source the handlers and the web client
// are written against, gzip-compressed: the document is 834 KB and compresses
// to a tenth of that. `make docs-gen` (cmd/docgen) regenerates it and
// TestEmbeddedSpecIsCurrent fails when it is stale.
//
//go:embed openapi.json.gz
var openapiSpecGz []byte

// openapiSpec returns the embedded document, inflated.
func openapiSpec() ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(openapiSpecGz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	return io.ReadAll(zr)
}

// The Scalar API reference is served from the binary: no CDN, so it works in
// air-gapped installs and under a strict CSP. scalarBundle is the standalone
// build of @scalar/api-reference (MIT), vendored gzip-compressed by
// scripts/update-scalar.sh; its pinned version is part of the file name.
const scalarBundleName = "scalar-1.76.0.js"

//go:embed apiref/scalar-1.76.0.js.gz
var scalarBundle []byte

//go:embed apiref/init.js
var scalarInit []byte

const (
	apiReferencePath = "/api/reference"
	scalarAssetDir   = apiReferencePath + "/assets/"

	// apiReferenceCSP lets the page run only same-origin scripts (Scalar
	// injects its stylesheet at runtime, hence the inline styles) and talk
	// only to its own origin.
	apiReferenceCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; " +
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

	apiReferenceHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>API reference</title>
<link rel="icon" href="data:,">
</head>
<body>
<div id="app"></div>
<script src="` + scalarAssetDir + scalarBundleName + `"></script>
<script src="` + scalarAssetDir + `init.js"></script>
</body>
</html>
`

	openapiLicenseName = "AGPL-3.0-or-later"
	openapiDescription = "REST API of the OpenArtifact artifact repository manager."
)

// OpenAPIHandler serves the OpenAPI spec and the interactive API reference.
type OpenAPIHandler struct {
	version     string
	docsEnabled bool

	patchOnce sync.Once
	specJSON  []byte
	specErr   error

	yamlOnce sync.Once
	specYAML []byte
	yamlErr  error

	jsonGz, yamlGz gzipVariant
}

// gzipVariant is the gzip form of one rendered document, made on the first
// request from a client that accepts gzip and kept: the spec is about 800 KB
// of text and every client that fetches it (code generators, the web client)
// accepts gzip.
type gzipVariant struct {
	once sync.Once
	data []byte
	err  error
}

func (g *gzipVariant) of(plain []byte) ([]byte, error) {
	g.once.Do(func() {
		var buf bytes.Buffer

		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if _, g.err = zw.Write(plain); g.err == nil {
			g.err = zw.Close()
		}

		g.data = buf.Bytes()
	})

	return g.data, g.err
}

// writeSpec answers with plain, or with its gzip form when the client accepts it.
func writeSpec(w http.ResponseWriter, r *http.Request, contentType string, plain []byte, gz *gzipVariant) {
	hd := w.Header()
	hd.Set("Content-Type", contentType)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Add("Vary", "Accept-Encoding")

	if acceptsGzip(r) {
		if packed, err := gz.of(plain); err == nil {
			hd.Set("Content-Encoding", "gzip")
			_, _ = w.Write(packed)

			return
		}
	}

	_, _ = w.Write(plain)
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// NewOpenAPIHandler returns a handler stamping version into the spec. The
// spec routes are always served; the API reference and the legacy /swagger
// redirects only when docsEnabled.
func NewOpenAPIHandler(version string, docsEnabled bool) *OpenAPIHandler {
	return &OpenAPIHandler{version: version, docsEnabled: docsEnabled}
}

func (h *OpenAPIHandler) RegisterRoutes(router chi.Router) {
	router.Get("/api/v1/openapi.json", h.handleSpec)
	router.Get("/api/v1/openapi.yaml", h.handleSpecYAML)
	router.Get(apiReferencePath, h.handleReference)
	router.Get(apiReferencePath+"/", h.handleReference)
	router.Get(scalarAssetDir+scalarBundleName, h.handleScalarBundle)
	router.Get(scalarAssetDir+"init.js", h.handleScalarInit)
	router.Get("/swagger", h.handleSwaggerRedirect)
	router.Get("/swagger/", h.handleSwaggerRedirect)
}

// patchedSpec returns the embedded spec with the build version, the licence
// and the description stamped into info, rendered once.
func (h *OpenAPIHandler) patchedSpec() ([]byte, error) {
	h.patchOnce.Do(func() {
		raw, err := openapiSpec()
		if err != nil {
			h.specErr = err
			return
		}

		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			h.specErr = err
			return
		}

		info, _ := doc["info"].(map[string]any)
		if info == nil {
			info = map[string]any{}
		}

		info["version"] = h.version
		info["description"] = openapiDescription
		info["license"] = map[string]any{"name": openapiLicenseName, "identifier": openapiLicenseName}
		doc["info"] = info

		h.specJSON, h.specErr = json.MarshalIndent(doc, "", "  ")
	})

	return h.specJSON, h.specErr
}

func (h *OpenAPIHandler) handleSpec(w http.ResponseWriter, r *http.Request) {
	out, err := h.patchedSpec()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to render OpenAPI spec")
		return
	}

	writeSpec(w, r, "application/json", out, &h.jsonGz)
}

// renderYAML converts the patched JSON spec to YAML once.
func (h *OpenAPIHandler) renderYAML() ([]byte, error) {
	h.yamlOnce.Do(func() {
		spec, err := h.patchedSpec()
		if err != nil {
			h.yamlErr = err
			return
		}

		var doc any
		if err := json.Unmarshal(spec, &doc); err != nil {
			h.yamlErr = err
			return
		}

		h.specYAML, h.yamlErr = yaml.Marshal(doc)
	})

	return h.specYAML, h.yamlErr
}

func (h *OpenAPIHandler) handleSpecYAML(w http.ResponseWriter, r *http.Request) {
	out, err := h.renderYAML()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to render OpenAPI spec")
		return
	}

	writeSpec(w, r, "application/yaml", out, &h.yamlGz)
}

// docsOr404 reports whether the API reference is enabled, answering 404 when not.
func (h *OpenAPIHandler) docsOr404(w http.ResponseWriter) bool {
	if h.docsEnabled {
		return true
	}

	writeError(w, http.StatusNotFound, "not_found", "the API reference is disabled")

	return false
}

func (h *OpenAPIHandler) handleSwaggerRedirect(w http.ResponseWriter, r *http.Request) {
	if !h.docsOr404(w) {
		return
	}

	http.Redirect(w, r, apiReferencePath, http.StatusMovedPermanently)
}

func (h *OpenAPIHandler) handleReference(w http.ResponseWriter, r *http.Request) {
	if !h.docsOr404(w) {
		return
	}

	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Content-Security-Policy", apiReferenceCSP)
	hd.Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, apiReferenceHTML)
}

func (h *OpenAPIHandler) handleScalarInit(w http.ResponseWriter, r *http.Request) {
	if !h.docsOr404(w) {
		return
	}

	hd := w.Header()
	hd.Set("Content-Type", "text/javascript; charset=utf-8")
	hd.Set("Cache-Control", "no-cache")
	_, _ = w.Write(scalarInit)
}

// handleScalarBundle serves the vendored bundle. The file name carries the
// pinned version, so the response is immutable. It is stored gzip-compressed
// and sent as is to clients that accept gzip.
func (h *OpenAPIHandler) handleScalarBundle(w http.ResponseWriter, r *http.Request) {
	if !h.docsOr404(w) {
		return
	}

	hd := w.Header()
	hd.Set("Content-Type", "text/javascript; charset=utf-8")
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Add("Vary", "Accept-Encoding")

	if acceptsGzip(r) {
		hd.Set("Content-Encoding", "gzip")
		_, _ = w.Write(scalarBundle)

		return
	}

	zr, err := gzip.NewReader(bytes.NewReader(scalarBundle))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read the API reference bundle")
		return
	}
	defer zr.Close()

	_, _ = io.Copy(w, zr) //nolint:gosec // embedded, trusted asset
}
