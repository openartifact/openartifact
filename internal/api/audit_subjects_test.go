// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/format/storage"
)

// auditKindSeq makes the format a test registers unique for the test binary:
// the storage registry is process-global and refuses a second registration.
var auditKindSeq atomic.Int64 //nolint:gochecknoglobals // test-binary-wide uniqueness counter

func TestIsAuditPackageKind(t *testing.T) {
	tests := []struct {
		resourceType string
		want         bool
	}{
		{"npm_version", true},
		{"deb_package", true},
		{"maven_artifact", true},
		{"docker_manifest", true},
		{"docker_tag", true},
		{"terraform_provider", true},
		{"repository", false},
		{"user", false},
		{"login", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run("type="+tc.resourceType, func(t *testing.T) {
			if got := isAuditPackageKind(tc.resourceType); got != tc.want {
				t.Errorf("isAuditPackageKind(%q) = %v, want %v", tc.resourceType, got, tc.want)
			}
		})
	}
}

// TestIsAuditPackageKindFollowsRegister pins that the cached kind set is not a
// startup snapshot: a format that registers later is a package kind from then on.
func TestIsAuditPackageKindFollowsRegister(t *testing.T) {
	kind := fmt.Sprintf("audit_late_kind_%d", auditKindSeq.Add(1))

	if isAuditPackageKind(kind) {
		t.Fatalf("%q is not registered yet and must not be a package kind", kind)
	}

	storage.Register("audit-late-"+kind, storage.Storage{Trash: storage.Trash{Kind: kind}})

	if !isAuditPackageKind(kind) {
		t.Fatalf("%q was registered and must be a package kind", kind)
	}

	if allocs := testing.AllocsPerRun(100, func() { _ = isAuditPackageKind(kind) }); allocs != 0 {
		t.Errorf("isAuditPackageKind allocates %v times per call, want 0", allocs)
	}
}
