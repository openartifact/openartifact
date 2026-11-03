// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// generated builds a document of every format for lib@1.0.0 with three
// dependencies, so each one is read back from what the generator really emits.
func generated(t *testing.T) map[string]*SBOMResult {
	t.Helper()

	meta := map[string]any{"dependencies": []any{
		map[string]any{"name": "left-pad", "version": "1.3.0", "license": "MIT"},
		map[string]any{"name": "util", "version": "2.0.0"},
		map[string]any{"name": "chalk", "version": "5.0.0", "license": "MIT"},
	}}

	g := newFakeGen(&fakeDB{})
	ctx := context.Background()
	out := map[string]*SBOMResult{}

	var err error

	if out["cyclonedx"], err = g.GenerateCycloneDX(ctx, "r", "lib@1.0.0", "npm", meta); err != nil {
		t.Fatal(err)
	}

	if out["spdx-json"], err = g.GenerateSPDXJSON(ctx, "r", "lib@1.0.0", "npm", meta); err != nil {
		t.Fatal(err)
	}

	if out["spdx3-json"], err = g.GenerateSPDX3JSON(ctx, "r", "lib@1.0.0", "npm", meta); err != nil {
		t.Fatal(err)
	}

	if out["spdx"], err = g.GenerateSPDX(ctx, "r", "lib@1.0.0", "npm", meta); err != nil {
		t.Fatal(err)
	}

	return out
}

func names(comps []Component) []string {
	out := make([]string, len(comps))
	for i, c := range comps {
		out[i] = c.Name
	}

	return out
}

func TestReadComponentsEveryFormat(t *testing.T) {
	for format, res := range generated(t) {
		t.Run(format, func(t *testing.T) {
			comps, truncated, err := ReadComponents(bytes.NewReader(res.Body), format, 100)
			if err != nil {
				t.Fatal(err)
			}

			if truncated {
				t.Error("truncated, want the whole list")
			}

			got := names(comps)
			for _, want := range []string{"left-pad", "util", "chalk"} {
				if !slices.Contains(got, want) {
					t.Errorf("components %v lack %q", got, want)
				}
			}

			i := slices.Index(got, "left-pad")
			if comps[i].Version != "1.3.0" {
				t.Errorf("left-pad version = %q", comps[i].Version)
			}

			if !strings.Contains(comps[i].PURL, "left-pad@1.3.0") {
				t.Errorf("left-pad purl = %q", comps[i].PURL)
			}

			if format != "spdx3-json" && !slices.Contains(comps[i].Licenses, "MIT") {
				t.Errorf("left-pad licenses = %v, want MIT", comps[i].Licenses)
			}
		})
	}
}

func TestReadComponentsTruncates(t *testing.T) {
	for format, res := range generated(t) {
		t.Run(format, func(t *testing.T) {
			all, _, err := ReadComponents(bytes.NewReader(res.Body), format, 100)
			if err != nil {
				t.Fatal(err)
			}

			comps, truncated, err := ReadComponents(bytes.NewReader(res.Body), format, 2)
			if err != nil {
				t.Fatal(err)
			}

			if len(comps) != 2 || !truncated {
				t.Errorf("got %d components, truncated=%v, want 2 and true", len(comps), truncated)
			}

			if !slices.Equal(names(comps), names(all)[:2]) {
				t.Errorf("components %v are not the first of %v", names(comps), names(all))
			}

			// A limit equal to the count is not a truncation.
			if _, truncated, err = ReadComponents(bytes.NewReader(res.Body), format, len(all)); err != nil || truncated {
				t.Errorf("limit == count: truncated=%v err=%v", truncated, err)
			}
		})
	}
}

func TestReadComponentsRejectsGarbage(t *testing.T) {
	for _, tc := range []struct{ format, doc string }{
		{"cyclonedx", "not json"},
		{"cyclonedx", `["a"]`},
		{"cyclonedx", `{"components": {"a": 1}}`},
		{"spdx-json", `{"packages": [1`},
		{"nope", `{}`},
	} {
		if _, _, err := ReadComponents(strings.NewReader(tc.doc), tc.format, 10); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s %q: err = %v, want ErrUnreadable", tc.format, tc.doc, err)
		}
	}
}

func TestReadComponentsDocumentWithoutList(t *testing.T) {
	comps, truncated, err := ReadComponents(strings.NewReader(`{"bomFormat":"CycloneDX"}`), "cyclonedx", 10)
	if err != nil || truncated || len(comps) != 0 {
		t.Errorf("got %v %v %v, want an empty list", comps, truncated, err)
	}
}
