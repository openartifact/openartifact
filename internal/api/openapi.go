// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

//go:embed openapi.json
var openapiSpec []byte

type OpenAPIHandler struct{}

func NewOpenAPIHandler() *OpenAPIHandler {
	return &OpenAPIHandler{}
}

func (h *OpenAPIHandler) RegisterRoutes(router chi.Router) {
	router.Get("/api/v1/openapi.json", h.handleSpec)
	router.Get("/api/v1/openapi.yaml", h.handleSpecYAML)
	router.Get("/swagger/", h.handleSwaggerUI)
	router.Get("/swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusMovedPermanently)
	})
}

func (h *OpenAPIHandler) handleSpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openapiSpec)
}

//nolint:gochecknoglobals // lazily rendered, immutable after first use
var (
	openapiYAMLOnce sync.Once
	openapiYAML     []byte
	errOpenapiYAML  error
)

// renderOpenAPIYAML converts the embedded JSON spec to YAML once.
func renderOpenAPIYAML() ([]byte, error) {
	openapiYAMLOnce.Do(func() {
		var doc any
		if err := json.Unmarshal(openapiSpec, &doc); err != nil {
			errOpenapiYAML = err
			return
		}

		openapiYAML, errOpenapiYAML = yaml.Marshal(doc)
	})

	return openapiYAML, errOpenapiYAML
}

func (h *OpenAPIHandler) handleSpecYAML(w http.ResponseWriter, r *http.Request) {
	out, err := renderOpenAPIYAML()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to render OpenAPI spec")
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(out)
}

func (h *OpenAPIHandler) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	// Serve inline Swagger UI HTML that loads from CDN
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerUIHTML))
}

const swaggerUIHTML = `<!DOCTYPE html>
<html>
<head>
  <title>OpenArtifact API</title>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5.34.0/swagger-ui.css" integrity="sha384-4c3vsE5f+trhFyHrFrLM1fFGUn531NIaC7dcqEWIJdoRQJ5CkJbSuSCwXLpaS1KF" crossorigin="anonymous">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5.34.0/swagger-ui-bundle.js" integrity="sha384-9Td7MKiIr2S83E1kZyyVi8KdJw/S8CpAvu0llLyMeg0xq/hDGiZJfVn0fV4llCmK" crossorigin="anonymous"></script>
<script>
window.onload = function() {
  SwaggerUIBundle({
    url: "/api/v1/openapi.json",
    dom_id: '#swagger-ui',
    presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
    layout: "BaseLayout"
  })
}
</script>
</body>
</html>`
