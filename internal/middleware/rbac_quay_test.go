// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuayAPIRoutesNeedGlobalManage(t *testing.T) {
	enabled := RBACConfig{QuayAPIPath: "/quay/api/v1"}

	tests := []struct {
		name   string
		cfg    RBACConfig
		method string
		path   string
		want   routeScope
	}{
		{"read", enabled, http.MethodGet, "/quay/api/v1/repository/konflux/a", routeScope{action: "manage"}},
		{"write", enabled, http.MethodPut, "/quay/api/v1/organization/konflux/robots/r", routeScope{action: "manage"}},
		{"root", enabled, http.MethodGet, "/quay/api/v1", routeScope{action: "manage"}},
		{"disabled falls back to repository routing", RBACConfig{}, http.MethodGet, "/quay/api/v1/repository/x", classifyRoute(
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/quay/api/v1/repository/x", nil))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, nil)

			if got := classifyRouteFor(req, tc.cfg); got != tc.want {
				t.Errorf("classifyRouteFor(%s %s) = %+v, want %+v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestQuayAPIPathIsNeverStatic(t *testing.T) {
	tests := []struct {
		name string
		cfg  RBACConfig
		path string
		want bool
	}{
		{"enabled prefix", RBACConfig{QuayAPIPath: "/quay/api/v1"}, "/quay/api/v1/repository", false},
		{"enabled root", RBACConfig{QuayAPIPath: "/quay/api/v1"}, "/quay/api/v1", false},
		{"sibling path stays static", RBACConfig{QuayAPIPath: "/quay/api/v1"}, "/quay/api/v10/x", true},
		{"disabled", RBACConfig{}, "/quay/api/v1/repository", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStaticPath(tc.path, tc.cfg); got != tc.want {
				t.Errorf("isStaticPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
