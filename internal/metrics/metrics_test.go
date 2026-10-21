// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"testing"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()

	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}

	if r.HTTPRequestsTotal == nil {
		t.Error("HTTPRequestsTotal is nil")
	}

	if r.HTTPRequestDurationSeconds == nil {
		t.Error("HTTPRequestDurationSeconds is nil")
	}

	if r.UploadsTotal == nil {
		t.Error("UploadsTotal is nil")
	}

	if r.BlobBytesTotal == nil {
		t.Error("BlobBytesTotal is nil")
	}

	if r.MetadataRegenerationsTotal == nil {
		t.Error("MetadataRegenerationsTotal is nil")
	}

	if r.MetadataRegenerationDurationSeconds == nil {
		t.Error("MetadataRegenerationDurationSeconds is nil")
	}

	if r.AuditDroppedTotal == nil {
		t.Error("AuditDroppedTotal is nil")
	}

	if r.TrashPurgedTotal == nil {
		t.Error("TrashPurgedTotal is nil")
	}

	// Verify counters can be used without panic
	r.HTTPRequestsTotal.WithLabelValues("GET", "/api/test", "200").Inc()
	r.UploadsTotal.WithLabelValues("npm", "success").Inc()
	r.BlobBytesTotal.WithLabelValues("write").Add(1024)
	r.MetadataRegenerationsTotal.WithLabelValues("deb").Inc()
	r.AuditDroppedTotal.Inc()
	r.TrashPurgedTotal.WithLabelValues("maven_artifact").Inc()
}
