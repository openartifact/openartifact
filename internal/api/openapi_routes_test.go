// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/pkg/logger"
)

// specOnlyAllowed lists spec operations served outside this package (root
// router, metrics, format handlers) and therefore not registered in the test.
func specOnlyAllowed() map[string]bool {
	return map[string]bool{
		"GET /health":        true,
		"GET /ready":         true,
		"GET /metrics":       true,
		"GET /api/v1/search": true, // registered by internal/search
	}
}

// routesIgnored lists API routes intentionally absent from the spec.
func routesIgnored() map[string]bool {
	return map[string]bool{
		"GET /api/v1/openapi.json": true,
		"GET /api/v1/openapi.yaml": true,
	}
}

// TestOpenAPISpecMatchesRoutes fails when a REST route registered by the
// handlers in this package is missing from openapi.json, or when the spec
// documents an /api/v1 operation that no handler serves.
func TestOpenAPISpecMatchesRoutes(t *testing.T) {
	r := chi.NewRouter()
	const repoPrefix = "/api/v1/repositories/{name}"

	NewAuthHandler(nil, nil, nil, nil).RegisterPublic(r)
	NewAuthHandler(nil, nil, nil, nil).RegisterAuthed(r)
	NewOIDCHandler(nil, nil, nil, nil, "").RegisterPublic(r)
	NewSAMLHandler(nil, nil, nil, nil, "", nil).RegisterPublic(r)
	sys := NewSystemInfoHandler("", "", "", nil, nil, "")
	sys.RegisterPublic(r)
	sys.RegisterAuthed(r)
	sso := NewAdminSSOHandler(nil, nil, nil, "", nil)
	sso.RegisterPublic(r)
	sso.RegisterAuthed(r)
	NewOpenAPIHandler().RegisterRoutes(r)
	NewAdminUsersHandler(nil, nil, nil).Register(r)
	NewAdminGroupsHandler(nil, nil, nil).Register(r)
	NewAdminPermissionsHandler(nil, nil, nil).Register(r)
	NewAdminTokensHandler(nil, nil, nil).Register(r)
	NewSelfTokensHandler(nil, nil, nil).Register(r)
	NewAdminAuditHandler(nil, nil, nil).Register(r)
	NewAdminStorageHandler(nil, nil, nil).Register(r)
	NewAdminTieringHandler(nil, nil, nil, nil, nil, nil).Register(r)
	NewAdminGPGHandler(nil, nil, nil).Register(r)
	NewAdminAIHandler(nil, nil, nil).Register(r)
	NewProjectsHandler(nil, nil, nil, nil).RegisterRoutes(r)
	NewRepositoriesHandler(nil, nil, nil).RegisterAuthed(r)
	NewCleanupHandler(nil, nil, nil, nil).RegisterAuthed(r)
	NewDockerGCHandler(nil, nil, nil, nil).RegisterAuthed(r)
	NewTrashHandler(nil, nil, nil).RegisterAuthed(r)
	NewBundlesHandler(nil, nil, nil).RegisterAuthed(r)
	NewSBOMHandler(nil, nil, nil, nil, nil).RegisterAuthed(r)
	NewVEXHandler(nil, nil, nil, nil).RegisterAuthed(r)
	NewWebhooksHandler(nil, nil, nil, nil).RegisterAuthed(r)
	NewBackupHandler(nil, nil, nil, nil).RegisterAuthed(r)
	NewLicenseHandler(nil, nil, nil, nil).RegisterRoutes(r)
	NewPresignedUploadHandler(nil, nil, nil, nil, 0, 0, nil).Register(r)
	NewSecretsHandler(nil, nil, nil).RegisterRoutes(r, repoPrefix)
	NewVulnHandler(nil, nil, nil, nil).RegisterRoutes(r, repoPrefix)
	NewVulnHandler(nil, nil, nil, nil).RegisterOverviewRoutes(r)
	NewQuotasHandler(nil, nil, nil).RegisterRoutes(r, repoPrefix)
	NewPropertiesHandler(nil, nil).RegisterRoutes(r, repoPrefix)
	NewAIHandler(nil, nil, nil, nil).RegisterRoutes(r, repoPrefix)
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	NewDebUploadHandler(log, nil, nil, nil).Register(r)
	NewRPMUploadHandler(log, nil, nil, nil).Register(r)
	NewDeltaHandler(log, nil, nil).Register(r)

	routes := map[string]bool{}
	err = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if route == "" {
			return nil
		}
		routes[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	httpMethods := map[string]bool{"get": true, "put": true, "post": true, "patch": true, "delete": true}
	for p, ops := range spec.Paths {
		for m := range ops {
			if httpMethods[m] {
				documented[strings.ToUpper(m)+" "+strings.TrimSuffix(p, "/")] = true
			}
		}
	}

	ignored, specOnly := routesIgnored(), specOnlyAllowed()

	var missing, stale []string
	for k := range routes {
		if strings.Contains(k, " /api/v1/") && !documented[k] && !ignored[k] {
			missing = append(missing, k)
		}
	}
	for k := range documented {
		if !routes[k] && !specOnly[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("routes missing from openapi.json:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("openapi.json operations without a route:\n  %s", strings.Join(stale, "\n  "))
	}
}
