// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package compactindex

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

const fuzzInfoSeed = "---\n1.0.0 |checksum:" + sumA + ",ruby:>= 2.0.0\n" +
	"1.0.0-java rake:>= 0&< 14,json:~> 2.0|checksum:" + sumB + ",ruby:> 0.0.0\n"

const fuzzVersionsSeed = "created_at: 2026-10-01T00:00:04Z\n---\nrake 12.0,13.0 " + md5A + "\njson 2.0 " + md5B + "\nrake 14.0,-12.0 " + md5B + "\n"

// FuzzParseInfo checks that Verify and Parse agree, that only ErrMalformed (or a
// read limit) is returned, and that a rendered file parses back to the same
// lines.
func FuzzParseInfo(f *testing.F) {
	for _, s := range []string{fuzzInfoSeed, "---\n", "---\n1.0 \n", "", "1.0\n", "---\n1.0 |checksum:zz\n", "---\n0.1 a:b|ruby:1\n\n"} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		lines, err := ParseInfoBytes(body)
		if verr := VerifyInfo(bytes.NewReader(body)); (err == nil) != (verr == nil) {
			t.Fatalf("Parse err %v, Verify err %v", err, verr)
		}

		if err != nil {
			return
		}

		again, err := ParseInfoBytes(RenderInfo(lines))
		if err != nil || !slices.Equal(again, lines) {
			t.Fatalf("render does not parse back: %v", err)
		}

		for _, l := range lines {
			if !ValidVersionField(l.Version) || (l.Checksum != "" && !ValidSHA256(l.Checksum)) || strings.ContainsAny(l.Raw, "\n") {
				t.Fatalf("accepted line %+v", l)
			}
		}
	})
}

// FuzzParseVersions checks that Verify and Parse agree and that a rendered
// versions file parses back to the same live entries.
func FuzzParseVersions(f *testing.F) {
	for _, s := range []string{fuzzVersionsSeed, "---\n", "created_at: x\n---\n", "", "---\nrake 1.0 " + md5A + "\n", "---\nrake -1.0 " + md5A + "\n"} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		v, err := ParseVersions(bytes.NewReader(body))
		if verr := VerifyVersions(bytes.NewReader(body)); (err == nil) != (verr == nil) {
			t.Fatalf("Parse err %v, Verify err %v", err, verr)
		}

		if err != nil {
			if !errors.Is(err, ErrMalformed) && !strings.Contains(err.Error(), "token too long") {
				t.Fatalf("unexpected error %v", err)
			}

			return
		}

		again, err := ParseVersions(bytes.NewReader(v.Render()))
		if err != nil {
			t.Fatalf("render does not parse back: %v", err)
		}

		live := slices.DeleteFunc(slices.Clone(v.Entries), func(e Entry) bool { return len(e.Versions) == 0 })
		slices.SortFunc(live, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })

		if len(live) != len(again.Entries) {
			t.Fatalf("entries %d != %d", len(live), len(again.Entries))
		}

		for i := range live {
			if live[i].Name != again.Entries[i].Name || !slices.Equal(live[i].Versions, again.Entries[i].Versions) ||
				live[i].InfoMD5 != again.Entries[i].InfoMD5 {
				t.Fatalf("entry %d changed: %+v -> %+v", i, live[i], again.Entries[i])
			}
		}
	})
}

// FuzzParseNames checks that Verify and Parse agree and that rendering sorts and
// de-duplicates without losing a name.
func FuzzParseNames(f *testing.F) {
	//nolint:dupword // a repeated name is a seed
	for _, s := range []string{"---\n_\n-\nrake\n", "---\n", "", "---\nrake\nrake\n", "---\nra ke\n"} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		names, err := ParseNames(bytes.NewReader(body))
		if verr := VerifyNames(bytes.NewReader(body)); (err == nil) != (verr == nil) {
			t.Fatalf("Parse err %v, Verify err %v", err, verr)
		}

		if err != nil {
			return
		}

		again, err := ParseNames(bytes.NewReader(RenderNames(names)))
		if err != nil || !slices.IsSorted(again) {
			t.Fatalf("render does not parse back sorted: %v", err)
		}

		for _, n := range names {
			if !slices.Contains(again, n) {
				t.Fatalf("name %q lost", n)
			}
		}
	})
}

// FuzzParsePath checks that every accepted path is canonical and that its file
// splits only into valid names and version fields.
func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"versions", "names", "info/rake", "info/.cat", "gems/rake-13.0.6.gem", "gems/nokogiri-1.16.0-x86_64-linux.gem", "gems/a-1-2.gem",
		"quick/Marshal.4.8/rake-13.0.6.gemspec.rz", "specs.4.8.gz", "info/..", "info/a/b", "gems/.gem", "", "/versions",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rel string) {
		p, ok := ParsePath(rel)
		if !ok {
			return
		}

		canonical := strings.TrimPrefix(rel, "/")
		if slices.ContainsFunc(strings.Split(canonical, "/"), func(seg string) bool { return seg == "" || seg == "." || seg == ".." }) {
			t.Fatalf("%q accepted with an empty or dot segment", rel)
		}

		if again, ok := ParsePath(canonical); !ok || again != p {
			t.Fatalf("not stable: %q", canonical)
		}

		switch p.Kind {
		case KindInfo:
			if !ValidName(p.Gem) || "info/"+p.Gem != canonical {
				t.Fatalf("info path %q -> %+v", rel, p)
			}
		case KindGem, KindGemspec:
			splits := SplitFile(p.File)
			if len(splits) == 0 {
				t.Fatalf("file %q accepted without a reading", p.File)
			}

			for _, s := range splits {
				if p.File != s.Name+"-"+s.Version || !ValidName(s.Name) || !ValidVersionField(s.Version) {
					t.Fatalf("bad split %+v of %q", s, p.File)
				}
			}
		case KindVersions, KindNames, KindSpecs:
		}
	})
}
