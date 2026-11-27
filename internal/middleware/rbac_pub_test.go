// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// `dart pub publish` makes two GETs around the upload POST (the upload URL and
// the finishing request); they belong to the publish and need "write", while the
// reads of the package API stay "read". The repository is the segment after the
// format marker.
func TestClassifyRoute_PubPublishStepsNeedWrite(t *testing.T) {
	const root = "/repos/pub/dart-repo/api/"

	tests := []struct {
		method, path, want string
	}{
		{http.MethodGet, root + "packages/versions/new", "write"},
		{http.MethodGet, root + "packages/versions/newUploadFinish?package=a&version=1.0.0", "write"},
		{http.MethodPost, root + "packages/versions/newUpload", "write"},
		{http.MethodGet, root + "packages/demo", "read"},
		{http.MethodGet, root + "packages/demo/versions/1.0.0", "read"},
		{http.MethodGet, root + "archives/demo-1.0.0.tar.gz", "read"},
		// Another format's path of the same shape is only a read.
		{http.MethodGet, "/repos/other/api/packages/versions/new", "read"},
	}

	for _, tc := range tests {
		req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)

		scope := classifyRoute(req)
		if scope.action != tc.want {
			t.Errorf("%s %s action = %q, want %q", tc.method, tc.path, scope.action, tc.want)
		}

		if tc.path[:len("/repos/pub/")] == "/repos/pub/" && scope.repoName != "dart-repo" {
			t.Errorf("%s %s repository = %q, want dart-repo", tc.method, tc.path, scope.repoName)
		}
	}
}
