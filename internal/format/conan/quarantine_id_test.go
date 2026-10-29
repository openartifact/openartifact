// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// listedRowID is the id of the listed row of a recipe revision: its manifest,
// the row the package listing shows and the scans are keyed by.
func (e *env) listedRowID(rrev string) string {
	e.t.Helper()

	for _, row := range e.db.rows {
		if row.RecipeRevision == rrev && row.PackageID == "" && row.Filename == manifestFile {
			return row.ID
		}
	}

	e.t.Fatalf("recipe revision %s has no manifest row", rrev)

	return ""
}

// TestDownloadQuarantinedByRowID: a scan keyed by the id of the listed row of a
// recipe revision blocks every file of that revision and of its binary
// packages, and no other revision.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	e := newEnv(t)
	e.publishRecipe(rrev1)
	e.publishRecipe(rrev2)
	e.publishPackage(rrev1, pid, prev1)

	id := e.listedRowID(rrev1)
	get := func(path string) int { return e.do(http.MethodGet, path, "").Code }

	for name, blocked := range map[string]string{
		"recipe file":  recipeFileURL(rrev1, "conan_export.tgz"),
		"manifest":     recipeFileURL(rrev1, manifestFile),
		"package file": packageFileURL(rrev1, pid, prev1, "conan_package.tgz"),
	} {
		t.Run(name, func(t *testing.T) {
			formattest.AssertQuarantinedByID(t, &e.db.Quarantine, id, get, blocked, recipeFileURL(rrev2, "conan_export.tgz"))
		})
	}
}

// TestDownloadQuarantinedByFileRowID: a scan keyed by the row id of one stored
// file blocks that file only.
func TestDownloadQuarantinedByFileRowID(t *testing.T) {
	e := newEnv(t)
	e.publishRecipe(rrev1)

	var id string

	for _, row := range e.db.rows {
		if row.Filename == "conan_export.tgz" {
			id = row.ID
		}
	}

	get := func(path string) int { return e.do(http.MethodGet, path, "").Code }

	formattest.AssertQuarantinedByID(t, &e.db.Quarantine, id, get,
		recipeFileURL(rrev1, "conan_export.tgz"), recipeFileURL(rrev1, "conanfile.py"))
}
