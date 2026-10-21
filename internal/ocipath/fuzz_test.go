// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocipath

import (
	"strings"
	"testing"
)

// FuzzParse feeds arbitrary /v2/ paths to the OCI path parser and checks the
// invariants the RBAC and docker handlers rely on.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"repo/app/manifests/latest",
		"repo/org/team/app/blobs/sha256:abc",
		"repo/app/blobs/uploads/",
		"repo/app/blobs/uploads/uuid",
		"repo/app/tags/list",
		"manifests/manifests/manifests",
		"../../etc/passwd",
		"",
		"/",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rest string) {
		req := Parse(rest)
		if req.Kind == KindUnknown {
			return
		}

		if !strings.HasPrefix(rest, req.Name) {
			t.Fatalf("Name %q is not a prefix of %q", req.Name, rest)
		}

		_, _ = SplitName(req.Name)
		_ = Resource(rest, req.Name)
		_ = ValidImage(req.Name)
	})
}
