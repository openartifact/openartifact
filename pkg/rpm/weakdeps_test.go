// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"os"
	"testing"
)

func depByName(deps []Dependency, name string) (Dependency, bool) {
	for _, d := range deps {
		if d.Name == name {
			return d, true
		}
	}

	return Dependency{}, false
}

func TestParseRPM_WeakDependencies(t *testing.T) {
	f, err := os.Open("testdata/weakdeps.rpm")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	pkg, err := ParseRPM(f)
	if err != nil {
		t.Fatalf("ParseRPM: %v", err)
	}

	checks := map[string][]Dependency{
		"oa-recommended":  pkg.Recommends,
		"oa-suggested":    pkg.Suggests,
		"oa-supplemented": pkg.Supplements,
		"oa-enhanced":     pkg.Enhances,
	}
	for name, deps := range checks {
		if _, ok := depByName(deps, name); !ok {
			t.Errorf("dependency %q missing from %v", name, deps)
		}
	}

	sug, _ := depByName(pkg.Suggests, "oa-suggested")
	if sug.Flags&SENSE_GREATEREQUAL != SENSE_GREATEREQUAL || sug.Version != "1.0" {
		t.Errorf("suggests flags/version = %#x/%q, want GE 1.0", sug.Flags, sug.Version)
	}

	if _, ok := depByName(pkg.Conflicts, "oa-conflicting"); !ok {
		t.Error("conflicts missing")
	}

	if _, ok := depByName(pkg.Obsoletes, "oa-old"); !ok {
		t.Error("obsoletes missing")
	}
}

func TestExtraRoundTripMarksWeakDeps(t *testing.T) {
	src := &Package{Recommends: []Dependency{{Name: "a"}}}

	data, err := src.MarshalExtra()
	if err != nil {
		t.Fatal(err)
	}

	var dst Package
	if err := dst.UnmarshalExtra(data); err != nil {
		t.Fatal(err)
	}

	if !dst.WeakDepsParsed || len(dst.Recommends) != 1 {
		t.Errorf("round trip lost weak deps: %+v", dst)
	}

	var legacy Package
	if err := legacy.UnmarshalExtra([]byte(`{"requires":[{"Name":"x"}]}`)); err != nil {
		t.Fatal(err)
	}

	if legacy.WeakDepsParsed {
		t.Error("legacy extra must not be marked as weak-deps parsed")
	}
}
