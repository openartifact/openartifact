// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// auditBenchPage is one page of audit rows: mostly package events (the kind
// whose resource type has to be matched against the format registry), the rest
// the other subjects of the view.
func auditBenchPage() ([]*models.AuditLog, *models.AuditNames) {
	types := []string{
		"npm_version", "maven_artifact", "deb_package", "pypi_package", "docker_tag", "go_module", "helm_chart",
		"user", "repository", "project", "token", "login", "", "system_setting", "rpm_package", "raw_artifact",
	}
	names := models.NewAuditNames()
	logs := make([]*models.AuditLog, 0, 100)

	for i := range 100 {
		rt := types[i%len(types)]
		rid := "00000000-0000-0000-0000-000000000001"
		logs = append(logs, &models.AuditLog{ResourceType: &rt, ResourceID: &rid})
	}

	return logs, names
}

// BenchmarkAuditSubject is the per-page cost of naming the subject of every
// audit row, the loop of the audit list endpoint.
func BenchmarkAuditSubject(b *testing.B) {
	logs, names := auditBenchPage()
	d := auditDetails{Package: "left-pad", Version: "1.3.0"}

	b.ReportAllocs()

	for b.Loop() {
		for _, l := range logs {
			if auditSubjectOf(l, d, names) == nil && l.ResourceType != nil && *l.ResourceType == "npm_version" {
				b.Fatal("a package event must name a subject")
			}
		}
	}
}

// BenchmarkIsAuditPackageKind isolates the registry lookup behind the subject.
func BenchmarkIsAuditPackageKind(b *testing.B) {
	kinds := []string{"npm_version", "deb_package", "user", "repository", "login", ""}

	b.ReportAllocs()

	for b.Loop() {
		for _, k := range kinds {
			_ = isAuditPackageKind(k)
		}
	}
}
