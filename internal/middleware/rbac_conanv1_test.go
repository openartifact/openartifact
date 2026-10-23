// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A Conan 1 client deletes with POSTs (`packages/delete`, `remove_files`); those
// must need "delete", not just the "write" every POST maps to, while the POSTs
// that only negotiate an upload stay "write".
func TestClassifyRoute_ConanV1PostDeletesNeedDelete(t *testing.T) {
	const recipe = "/repos/conan-repo/v1/conans/zlib/1.3/_/_"

	tests := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodPost, recipe + "/packages/delete", "delete"},
		{http.MethodPost, recipe + "/remove_files", "delete"},
		{http.MethodPost, recipe + "/upload_urls", "write"},
		{http.MethodPost, recipe + "/packages/abc123/upload_urls", "write"},
		{http.MethodDelete, recipe, "delete"},
		{http.MethodGet, recipe + "/download_urls", "read"},
		// Not under v1/conans: the suffix alone decides nothing.
		{http.MethodPost, "/repos/other/some/remove_files", "write"},
	}

	for _, tc := range tests {
		req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
		if got := classifyRoute(req).action; got != tc.want {
			t.Errorf("%s %s action = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
