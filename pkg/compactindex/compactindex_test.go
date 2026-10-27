// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package compactindex

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	require.Len(t, lines, 3)

	assert.Equal(t, "1.0.0", lines[0].Version)
	assert.Equal(t, sumA, lines[0].Checksum)
	assert.Equal(t, "1.0.0-java", lines[1].Version)
	assert.Equal(t, sumB, lines[1].Checksum)
	assert.Empty(t, lines[2].Checksum)

	l, ok := FindInfo(lines, "1.0.0-java")
	require.True(t, ok)
	assert.Equal(t, lines[1], l)

	_, ok = FindInfo(lines, "2.0")
	assert.False(t, ok)

	assert.Equal(t, strings.TrimSuffix(body, "0.9 \n")+"0.9 \n", string(RenderInfo(lines)))
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
		"long line":     "---\n1.0 " + strings.Repeat("a", MaxLineBytes) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseInfoBytes([]byte(body))
			require.Error(t, err)
			assert.Error(t, VerifyInfo(strings.NewReader(body)))
		})
	}
}

func TestMergeInfo(t *testing.T) {
	t.Parallel()

	a, err := ParseInfoBytes([]byte("---\n1.0 |checksum:" + sumA + "\n2.0 |checksum:" + sumA + "\n"))
	require.NoError(t, err)

	b, err := ParseInfoBytes([]byte("---\n2.0 |checksum:" + sumB + "\n3.0 |checksum:" + sumB + "\n"))
	require.NoError(t, err)

	merged := MergeInfo(a, b)
	require.Len(t, merged, 3)
	assert.Equal(t, "2.0", merged[1].Version)
	assert.Equal(t, sumA, merged[1].Checksum, "the first source wins a duplicate version")
	assert.Equal(t, "3.0", merged[2].Version)
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
	require.NoError(t, err)
	assert.Equal(t, "2026-10-01T00:00:04Z", v.CreatedAt)
	require.Len(t, v.Entries, 3)
	assert.Equal(t, Entry{Name: "rake", Versions: []string{"13.0", "14.0"}, InfoMD5: md5B}, v.Entries[0])
	assert.Equal(t, "json", v.Entries[1].Name)
	assert.Empty(t, v.Entries[2].Versions)

	assert.Equal(t, "created_at: 2026-10-01T00:00:04Z\n---\n"+
		"json 2.0 "+md5B+"\n"+
		"rake 13.0,14.0 "+md5B+"\n", string(v.Render()))
	require.NoError(t, VerifyVersions(strings.NewReader(body)))
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
			require.ErrorIs(t, err, ErrMalformed)
			require.ErrorIs(t, VerifyVersions(strings.NewReader(body)), ErrMalformed)
		})
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	names, err := ParseNames(strings.NewReader("---\n_\n-\nrake\n.cat\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"_", "-", "rake", ".cat"}, names)
	assert.Equal(t, "---\n-\n.cat\n_\nrake\n", string(RenderNames(append(names, "rake"))))

	for _, body := range []string{"", "rake\n", "---\nra ke\n", "---\n..\n", "---\n\n"} {
		_, err := ParseNames(strings.NewReader(body))
		require.ErrorIs(t, err, ErrMalformed, body)
		require.ErrorIs(t, VerifyNames(strings.NewReader(body)), ErrMalformed, body)
	}
}

func TestInfoMD5(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", InfoMD5(nil))
	assert.Equal(t, "5eb63bbbe01eeed093cb22bb8f5acdc3", InfoMD5([]byte("hello world")))
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
		require.True(t, ok, rel)
		assert.Equal(t, want, got, rel)
	}

	for _, rel := range []string{
		"", "info/", "info/..", "info/a/b", "info/a b", "gems/", "gems/.gem", "gems/rake.gem", "gems/rake-.gem", "gems/rake-1.0.zip",
		"quick/Marshal.4.8/rake-1.gemspec", "quick/Marshal.4.8/rake-1.0.gem", "versions/", "api/v1/dependencies", "../versions",
	} {
		_, ok := ParsePath(rel)
		assert.False(t, ok, rel)
	}
}

func TestSplitFile(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []FileSplit{{"rake", "13.0.6"}}, SplitFile("rake-13.0.6"))
	assert.Equal(t, []FileSplit{{"foo-bar", "1.0-java"}}, SplitFile("foo-bar-1.0-java"))
	assert.Equal(t, []FileSplit{{"a", "1-2"}, {"a-1", "2"}}, SplitFile("a-1-2"))
	assert.Equal(t, []FileSplit{{"nokogiri", "1.16.0-x86_64-linux"}}, SplitFile("nokogiri-1.16.0-x86_64-linux"))
	assert.Empty(t, SplitFile("rake"))
	assert.Empty(t, SplitFile("rake-"))
	assert.Empty(t, SplitFile("-1.0"))
}

func TestErrMalformedIsWrapped(t *testing.T) {
	t.Parallel()

	_, err := ParseInfoBytes(bytes.Repeat([]byte("x"), 3))
	assert.ErrorIs(t, err, ErrMalformed)
}

func TestParseVersionsAcceptsLineWithoutMD5(t *testing.T) {
	// rubygems.org lists some platform releases with an empty checksum field.
	v, err := ParseVersions(strings.NewReader("---\nyeptris 0.6.28.3-x86_64-linux \nrake 1.0 " + md5A + "\n"))
	require.NoError(t, err)
	require.Len(t, v.Entries, 2)
	assert.Empty(t, v.Entries[0].InfoMD5)
	assert.Contains(t, string(v.Render()), "yeptris 0.6.28.3-x86_64-linux \n")
}
