// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestUploadHonorsImportCreatedAt: every file of an upload carrying an origin
// creation time (as importmeta resolves it for a manage principal) is stored
// with it as created_at; an ordinary upload is stamped by the database (the
// fake's clock), never with the origin.
func TestUploadHonorsImportCreatedAt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		at       time.Time
		imported bool
	}{
		{"imported", formattest.Origin(), true},
		{"normal upload", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			register := formattest.AtOrigin(e.h.RegisterRepoRoutes, tc.at)

			put := func(target, body string) {
				t.Helper()

				w := formattest.Do(register, formattest.Request(http.MethodPut, target, strings.NewReader(body)))
				if w.Code != http.StatusCreated {
					t.Fatalf("PUT %s = %d: %s", target, w.Code, w.Body.String())
				}
			}

			for _, f := range []string{"conan_export.tgz", "conan_sources.tgz", "conanfile.py", "conanmanifest.txt"} {
				put(recipeFileURL(rrev1, f), f)
			}

			for _, f := range []string{"conan_package.tgz", "conaninfo.txt", "conanmanifest.txt"} {
				put(packageFileURL(rrev1, pid, prev1, f), f)
			}

			if len(e.db.rows) != 7 {
				t.Fatalf("rows = %d, want 7", len(e.db.rows))
			}

			for _, row := range e.db.rows {
				if got := row.CreatedAt.Equal(formattest.Origin()); got != tc.imported {
					t.Errorf("%s created_at = %v, origin match = %v, want %v", row.Filename, row.CreatedAt, got, tc.imported)
				}
			}
		})
	}
}
