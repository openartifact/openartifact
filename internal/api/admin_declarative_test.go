// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestValidateDeclarativeItem(t *testing.T) {
	valid := models.DeclarativeObject{Kind: "Project", Name: "acme", ObjectID: "id-1", AppliedHash: "abc"}

	cases := []struct {
		name   string
		mutate func(*models.DeclarativeObject)
		valid  bool
	}{
		{"valid item", func(*models.DeclarativeObject) {}, true},
		{"unknown kind", func(o *models.DeclarativeObject) { o.Kind = "Bogus" }, false},
		{"empty name", func(o *models.DeclarativeObject) { o.Name = "" }, false},
		{"empty object id", func(o *models.DeclarativeObject) { o.ObjectID = "" }, false},
		{"NUL in kind", func(o *models.DeclarativeObject) { o.Kind = "Pro\x00ject" }, false},
		{"NUL in name", func(o *models.DeclarativeObject) { o.Name = "a\x00b" }, false},
		{"NUL in object id", func(o *models.DeclarativeObject) { o.ObjectID = "a\x00b" }, false},
		{"NUL in applied hash", func(o *models.DeclarativeObject) { o.AppliedHash = "a\x00b" }, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := valid
			tc.mutate(&it)

			if msg := validateDeclarativeItem(it); (msg == "") != tc.valid {
				t.Fatalf("validateDeclarativeItem = %q, want valid=%v", msg, tc.valid)
			}
		})
	}
}
