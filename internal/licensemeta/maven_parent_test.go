// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/openartifact/openartifact/internal/licensemeta"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

func pomOf(parent *pkgmaven.Parent, names ...string) *pkgmaven.POM {
	p := &pkgmaven.POM{Parent: parent}
	for _, n := range names {
		p.Licenses = append(p.Licenses, pkgmaven.License{Name: n})
	}

	return p
}

func ref(a string) *pkgmaven.Parent {
	return &pkgmaven.Parent{GroupID: "g", ArtifactID: a, Version: "1"}
}

func mapLookup(poms map[string]*pkgmaven.POM, calls *int) licensemeta.POMLookup {
	return func(_ context.Context, g, a, v string) (*pkgmaven.POM, error) {
		*calls++

		return poms[g+":"+a+":"+v], nil
	}
}

func TestInheritPOMLicenses(t *testing.T) {
	t.Parallel()

	chain := map[string]*pkgmaven.POM{
		"g:mid:1":  pomOf(ref("root")),
		"g:root:1": pomOf(nil, "Apache-2.0"),
		"g:a:1":    pomOf(ref("b")),
		"g:b:1":    pomOf(ref("a")),
		"g:bare:1": pomOf(nil),
	}

	tests := []struct {
		name         string
		parent       *pkgmaven.Parent
		want         []string
		wantResolved bool
	}{
		{"two levels", ref("mid"), []string{"Apache-2.0"}, true},
		{"direct", ref("root"), []string{"Apache-2.0"}, true},
		{"cycle", ref("a"), nil, false},
		{"missing", ref("nope"), nil, false},
		{"root without license", ref("bare"), nil, true},
		{"property version", &pkgmaven.Parent{GroupID: "g", ArtifactID: "root", Version: "${revision}"}, nil, false},
		{"no parent", nil, nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var calls int

			got, resolved, err := licensemeta.InheritPOMLicenses(t.Context(), tc.parent, mapLookup(chain, &calls))
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(got, tc.want) || resolved != tc.wantResolved {
				t.Fatalf("got %v resolved=%v, want %v resolved=%v", got, resolved, tc.want, tc.wantResolved)
			}
		})
	}
}

func TestInheritPOMLicensesDepthBound(t *testing.T) {
	t.Parallel()

	poms := map[string]*pkgmaven.POM{}
	n := licensemeta.MaxParentDepth + 3

	for i := range n {
		var next *pkgmaven.Parent
		if i < n-1 {
			next = ref("p" + strconv.Itoa(i+1))
		}

		if i == n-1 {
			poms["g:p"+strconv.Itoa(i)+":1"] = pomOf(nil, "MIT")
			continue
		}

		poms["g:p"+strconv.Itoa(i)+":1"] = pomOf(next)
	}

	var calls int

	got, resolved, err := licensemeta.InheritPOMLicenses(t.Context(), ref("p0"), mapLookup(poms, &calls))
	if err != nil || resolved || len(got) != 0 {
		t.Fatalf("got %v resolved=%v err=%v, want unresolved", got, resolved, err)
	}

	if calls != licensemeta.MaxParentDepth {
		t.Fatalf("lookups = %d, want %d", calls, licensemeta.MaxParentDepth)
	}
}

func TestInheritPOMLicensesLookupError(t *testing.T) {
	t.Parallel()

	boom := errors.New("storage down")

	_, _, err := licensemeta.InheritPOMLicenses(t.Context(), ref("x"), func(context.Context, string, string, string) (*pkgmaven.POM, error) {
		return nil, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the lookup error", err)
	}
}
