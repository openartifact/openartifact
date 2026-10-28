// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package compactindex

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	sumA = "ceab46efc69e21259f97393a92d1f4ec29582ec1137545183abbd11b26459e94"
	sumB = "0e0d9357893053a369e7f9f3fc13eb4a61aff3058dd419fcb61f73d762e0b9ed"
	md5A = "419d8a97f5fa53e83192b142e0fd648b"
	md5B = "eca1e0372184d5a0a32c664273a41dc4"
)

func TestParseInfo(t *testing.T) {
	t.Parallel()

	body := "---\n" +
		"1.0.0 |checksum:" + sumA + ",ruby:>= 2.0.0\n" +
		"1.0.0-java rake:>= 0&< 14,json:~> 2.0|checksum:" + sumB + ",ruby:> 0.0.0,created_at:2009-07-25T18:01:32Z\n" +
		"0.9 \n"

	lines, err := ParseInfoBytes([]byte(body))
	noErr(t, err)
	length(t, lines, 3)

	equal(t, "1.0.0", lines[0].Version)
	equal(t, sumA, lines[0].Checksum)
	equal(t, "1.0.0-java", lines[1].Version)
	equal(t, sumB, lines[1].Checksum)
	empty(t, lines[2].Checksum)

	l, ok := FindInfo(lines, "1.0.0-java")
	isTrue(t, ok)
	equal(t, lines[1], l)

	_, ok = FindInfo(lines, "2.0")
	isFalse(t, ok)

	equal(t, strings.TrimSuffix(body, "0.9 \n")+"0.9 \n", string(RenderInfo(lines)))
}

func TestParseInfoRejects(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"empty":         "",
		"no separator":  "1.0 |checksum:" + sumA + "\n",
		"bad version":   "---\nx1.0 |checksum:" + sumA + "\n",
		"empty version": "---\n |checksum:" + sumA + "\n",
		"short sum":     "---\n1.0 |checksum:abcd\n",
		"uppercase sum": "---\n1.0 |checksum:" + strings.ToUpper(sumA) + "\n",
		"control":       "---\n1.0\x00 |checksum:" + sumA + "\n",
		"double CR":     "---\n0 \r\r",
		"long line":     "---\n1.0 " + strings.Repeat("a", MaxLineBytes) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseInfoBytes([]byte(body))
			hasErr(t, err)
			hasErr(t, VerifyInfo(strings.NewReader(body)))
		})
	}
}

func TestMergeInfo(t *testing.T) {
	t.Parallel()

	a, err := ParseInfoBytes([]byte("---\n1.0 |checksum:" + sumA + "\n2.0 |checksum:" + sumA + "\n"))
	noErr(t, err)

	b, err := ParseInfoBytes([]byte("---\n2.0 |checksum:" + sumB + "\n3.0 |checksum:" + sumB + "\n"))
	noErr(t, err)

	merged := MergeInfo(a, b)
	length(t, merged, 3)
	equal(t, "2.0", merged[1].Version)
	equal(t, sumA, merged[1].Checksum, "the first source wins a duplicate version")
	equal(t, "3.0", merged[2].Version)
}

func TestParseVersions(t *testing.T) {
	t.Parallel()

	body := "created_at: 2026-10-01T00:00:04Z\n---\n" +
		"rake 12.0,13.0 " + md5A + "\n" +
		"json 2.0 " + md5B + "\n" +
		"rake 14.0,-12.0 " + md5B + "\n" +
		"gone 1.0 " + md5A + "\n" +
		"gone -1.0 " + md5A + "\n"

	v, err := ParseVersions(strings.NewReader(body))
	noErr(t, err)
	equal(t, "2026-10-01T00:00:04Z", v.CreatedAt)
	length(t, v.Entries, 3)
	equal(t, Entry{Name: "rake", Versions: []string{"13.0", "14.0"}, InfoMD5: md5B}, v.Entries[0])
	equal(t, "json", v.Entries[1].Name)
	empty(t, v.Entries[2].Versions)

	equal(t, "created_at: 2026-10-01T00:00:04Z\n---\n"+
		"json 2.0 "+md5B+"\n"+
		"rake 13.0,14.0 "+md5B+"\n", string(v.Render()))
	noErr(t, VerifyVersions(strings.NewReader(body)))
}

func TestParseVersionsRejects(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"no separator": "created_at: x\nrake 1.0 " + md5A + "\n",
		"two fields":   "---\nrake 1.0\n",
		"four fields":  "---\nrake 1.0 " + md5A + " x\n",
		"bad md5":      "---\nrake 1.0 zz\n",
		"bad version":  "---\nrake x " + md5A + "\n",
		"bad name":     "---\nra/ke 1.0 " + md5A + "\n",
		"empty token":  "---\nrake 1.0, " + md5A + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseVersions(strings.NewReader(body))
			errIs(t, err, ErrMalformed)
			errIs(t, VerifyVersions(strings.NewReader(body)), ErrMalformed)
		})
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	names, err := ParseNames(strings.NewReader("---\n_\n-\nrake\n.cat\n"))
	noErr(t, err)
	equal(t, []string{"_", "-", "rake", ".cat"}, names)
	equal(t, "---\n-\n.cat\n_\nrake\n", string(RenderNames(append(names, "rake"))))

	for _, body := range []string{"", "rake\n", "---\nra ke\n", "---\n..\n", "---\n\n"} {
		_, err := ParseNames(strings.NewReader(body))
		errIs(t, err, ErrMalformed, body)
		errIs(t, VerifyNames(strings.NewReader(body)), ErrMalformed, body)
	}
}

func TestInfoMD5(t *testing.T) {
	t.Parallel()

	equal(t, "d41d8cd98f00b204e9800998ecf8427e", InfoMD5(nil))
	equal(t, "5eb63bbbe01eeed093cb22bb8f5acdc3", InfoMD5([]byte("hello world")))
}

func TestParsePath(t *testing.T) {
	t.Parallel()

	for rel, want := range map[string]Path{
		"versions":                         {Kind: KindVersions},
		"/names":                           {Kind: KindNames},
		"info/rake":                        {Kind: KindInfo, Gem: "rake"},
		"info/.cat":                        {Kind: KindInfo, Gem: ".cat"},
		"gems/rake-13.0.6.gem":             {Kind: KindGem, File: "rake-13.0.6"},
		"quick/Marshal.4.8/a-1.gemspec.rz": {Kind: KindGemspec, File: "a-1"},
		"specs.4.8.gz":                     {Kind: KindSpecs, File: "specs.4.8.gz"},
		"latest_specs.4.8.gz":              {Kind: KindSpecs, File: "latest_specs.4.8.gz"},
		"prerelease_specs.4.8.gz":          {Kind: KindSpecs, File: "prerelease_specs.4.8.gz"},
	} {
		got, ok := ParsePath(rel)
		isTrue(t, ok, rel)
		equal(t, want, got, rel)
	}

	for _, rel := range []string{
		"", "info/", "info/..", "info/a/b", "info/a b", "gems/", "gems/.gem", "gems/rake.gem", "gems/rake-.gem", "gems/rake-1.0.zip",
		"quick/Marshal.4.8/rake-1.gemspec", "quick/Marshal.4.8/rake-1.0.gem", "versions/", "api/v1/dependencies", "../versions",
	} {
		_, ok := ParsePath(rel)
		isFalse(t, ok, rel)
	}
}

func TestSplitFile(t *testing.T) {
	t.Parallel()

	equal(t, []FileSplit{{"rake", "13.0.6"}}, SplitFile("rake-13.0.6"))
	equal(t, []FileSplit{{"foo-bar", "1.0-java"}}, SplitFile("foo-bar-1.0-java"))
	equal(t, []FileSplit{{"a", "1-2"}, {"a-1", "2"}}, SplitFile("a-1-2"))
	equal(t, []FileSplit{{"nokogiri", "1.16.0-x86_64-linux"}}, SplitFile("nokogiri-1.16.0-x86_64-linux"))
	empty(t, SplitFile("rake"))
	empty(t, SplitFile("rake-"))
	empty(t, SplitFile("-1.0"))
}

func TestErrMalformedIsWrapped(t *testing.T) {
	t.Parallel()

	_, err := ParseInfoBytes(bytes.Repeat([]byte("x"), 3))
	errIs(t, err, ErrMalformed)
}

func TestParseVersionsAcceptsLineWithoutMD5(t *testing.T) {
	// rubygems.org lists some platform releases with an empty checksum field.
	v, err := ParseVersions(strings.NewReader("---\nyeptris 0.6.28.3-x86_64-linux \nrake 1.0 " + md5A + "\n"))
	noErr(t, err)
	length(t, v.Entries, 2)
	empty(t, v.Entries[0].InfoMD5)
	contains(t, string(v.Render()), "yeptris 0.6.28.3-x86_64-linux \n")
}

func noErr(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func hasErr(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected an error")
	}
}

func errIs(t *testing.T, err, target error, msg ...any) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error %v is not %v %v", err, target, msg)
	}
}

func length[T any](t *testing.T, s []T, n int) {
	t.Helper()

	if len(s) != n {
		t.Fatalf("length %d, want %d", len(s), n)
	}
}

func isTrue(t *testing.T, ok bool, msg ...any) {
	t.Helper()

	if !ok {
		t.Fatalf("expected true %v", msg)
	}
}

func isFalse(t *testing.T, ok bool, msg ...any) {
	t.Helper()

	if ok {
		t.Fatalf("expected false %v", msg)
	}
}

func equal(t *testing.T, want, got any, msg ...any) {
	t.Helper()

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("got %#v, want %#v %v", got, want, msg)
	}
}

func empty(t *testing.T, v any) {
	t.Helper()

	if rv := reflect.ValueOf(v); rv.IsValid() && rv.Len() != 0 {
		t.Fatalf("expected empty, got %#v", v)
	}
}

func contains(t *testing.T, s, sub string) {
	t.Helper()

	if !strings.Contains(s, sub) {
		t.Fatalf("%q does not contain %q", s, sub)
	}
}
