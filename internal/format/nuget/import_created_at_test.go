// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// TestPushHonorsImportCreatedAt: a push carrying an origin creation time (as
// importmeta resolves it for a manage principal) stores it as created_at; an
// ordinary push leaves it unset so the database stamps now.
func TestPushHonorsImportCreatedAt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		at, want time.Time
	}{
		{"imported", formattest.Origin(), formattest.Origin()},
		{"normal upload", time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _, _ := newTestHandler(t)

			var body bytes.Buffer

			mw := multipart.NewWriter(&body)
			fw, _ := mw.CreateFormFile("package", "x.nupkg")
			_, _ = fw.Write(createNupkg(t, "Newtonsoft.Json.nuspec", sampleNuspec))
			_ = mw.Close()

			req := formattest.Request(http.MethodPut, "/repos/myrepo/v3/package", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())

			if w := formattest.Do(formattest.AtOrigin(register(h), tc.at), req); w.Code != http.StatusCreated {
				t.Fatalf("push status = %d: %s", w.Code, w.Body.String())
			}

			if db.pkg == nil || !db.pkg.CreatedAt.Equal(tc.want) {
				t.Fatalf("pkg = %+v, want created_at %v", db.pkg, tc.want)
			}
		})
	}
}
