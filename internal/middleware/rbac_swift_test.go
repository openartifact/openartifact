// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// "swift package-registry login" POSTs to /login to check the credential: that
// needs "read" (the credential is for downloads too), while the publish PUT
// stays "write". Only the login of a swift repository is read-only.
func TestClassifyRoute_SwiftLoginNeedsRead(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodPost, "/repos/swift/pkgs/login", "read"},
		{http.MethodPut, "/repos/swift/pkgs/mona/LinkedList/1.0.0", "write"},
		{http.MethodDelete, "/repos/swift/pkgs/mona/LinkedList/1.0.0", "delete"},
		{http.MethodGet, "/repos/swift/pkgs/mona/LinkedList", "read"},
		// Another format's POST /login stays a write.
		{http.MethodPost, "/repos/raw/pkgs/login", "write"},
		{http.MethodPost, "/repos/pkgs/login", "write"},
		// A package named login/x is not the login endpoint.
		{http.MethodPost, "/repos/swift/pkgs/login/x", "write"},
	}

	for _, tc := range tests {
		req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
		if got := classifyRoute(req).action; got != tc.want {
			t.Errorf("%s %s action = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
