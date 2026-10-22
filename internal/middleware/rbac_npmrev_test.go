// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// `npm unpublish` PUTs a reduced packument to /pkg/-rev/<rev>; versions missing
// from the body are removed, so the route must need "delete", not "write".
func TestClassifyRoute_NPMRevPutNeedsDelete(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodPut, "/repos/npm-repo/lodash/-rev/1-abc", "delete"},
		{http.MethodPut, "/repos/npm-repo/@scope%2Fname/-rev/2-abc", "delete"},
		{http.MethodPut, "/repos/npm-repo/lodash", "write"},
		{http.MethodDelete, "/repos/npm-repo/lodash/-rev/1-abc", "delete"},
		{http.MethodGet, "/repos/npm-repo/lodash/-rev/1-abc", "read"},
	}

	for _, tc := range tests {
		req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
		if got := classifyRoute(req).action; got != tc.want {
			t.Errorf("%s %s action = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
