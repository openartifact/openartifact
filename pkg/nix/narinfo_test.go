// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"testing"
)

const (
	testStorePath = StoreDir + "/" + testHash + "-hello-2.12.1"
	testRefA      = "00000000000000000000000000000000-aaa"
	testRefB      = "11111111111111111111111111111111-bbb"
)

// testFileHash() is the nixbase32 hash that names the NAR file in fixtures.
func testFileHash() SHA256 {
	b, err := DecodeBase32(emptySHA256Base32, sha256.Size)
	if err != nil {
		panic(err)
	}

	var s SHA256

	copy(s[:], b)

	return s
}

func narInfoText(extra ...string) string {
	lines := []string{
		"StorePath: " + testStorePath,
		"URL: nar/" + emptySHA256Base32 + ".nar.xz",
		"Compression: xz",
		"FileHash: sha256:" + emptySHA256Base32,
		"FileSize: 1234",
		"NarHash: sha256:" + emptySHA256Base32,
		"NarSize: 5678",
		"References: " + testRefB + " " + testRefA,
		"Deriver: " + testHash + "-hello-2.12.1.drv",
		"Sig: test-1:AAAA",
	}

	return strings.Join(append(lines, extra...), "\n") + "\n"
}

func sameNarInfo(a, b *NarInfo) bool {
	return a.StorePath == b.StorePath && a.URL == b.URL && a.Compression == b.Compression &&
		a.FileHash == b.FileHash && a.FileSize == b.FileSize && a.NarHash == b.NarHash &&
		a.NarSize == b.NarSize && a.Deriver == b.Deriver && a.CA == b.CA &&
		slices.Equal(a.References, b.References) && slices.Equal(a.Sigs, b.Sigs)
}

func TestParseNarInfo(t *testing.T) {
	n, err := ParseNarInfo([]byte(narInfoText("CA: fixed:r:sha256:abc")))
	if err != nil {
		t.Fatal(err)
	}

	want := &NarInfo{
		StorePath:   testStorePath,
		URL:         "nar/" + emptySHA256Base32 + ".nar.xz",
		Compression: "xz",
		FileHash:    "sha256:" + emptySHA256Base32,
		FileSize:    1234,
		NarHash:     "sha256:" + emptySHA256Base32,
		NarSize:     5678,
		References:  []string{testRefB, testRefA},
		Deriver:     testHash + "-hello-2.12.1.drv",
		Sigs:        []string{"test-1:AAAA"},
		CA:          "fixed:r:sha256:abc",
	}

	if !sameNarInfo(n, want) {
		t.Fatalf("parsed %+v, want %+v", n, want)
	}

	if n.StoreHash() != testHash || n.Name() != "hello-2.12.1" {
		t.Fatalf("StoreHash/Name = %q, %q", n.StoreHash(), n.Name())
	}
}

func TestNarInfoRoundTrip(t *testing.T) {
	n, err := ParseNarInfo([]byte(narInfoText("Sig: test-2:BBBB", "CA: fixed:r:sha256:abc")))
	if err != nil {
		t.Fatal(err)
	}

	text := n.String()

	again, err := ParseNarInfo([]byte(text))
	if err != nil {
		t.Fatalf("rendered narinfo does not parse: %v\n%s", err, text)
	}

	if !sameNarInfo(n, again) || again.String() != text {
		t.Fatalf("round trip changed the narinfo:\n%s\n---\n%s", text, again.String())
	}

	if len(again.Sigs) != 2 {
		t.Fatalf("Sigs = %v, want 2", again.Sigs)
	}
}

func TestParseNarInfoDefaults(t *testing.T) {
	text := "StorePath: " + testStorePath + "\nURL: nar/x.nar\nNarHash: sha256:" + emptySHA256Base32 +
		"\nNarSize: 1\nDeriver: unknown\nReferences:\n"

	n, err := ParseNarInfo([]byte(text))
	if err != nil {
		t.Fatal(err)
	}

	if n.Compression != "bzip2" {
		t.Errorf("Compression = %q, want bzip2", n.Compression)
	}

	if n.Deriver != "" {
		t.Errorf("Deriver = %q, want empty", n.Deriver)
	}

	if len(n.References) != 0 {
		t.Errorf("References = %v, want none", n.References)
	}

	if strings.Contains(n.String(), "Deriver") {
		t.Errorf("rendered narinfo mentions the unknown deriver:\n%s", n.String())
	}
}

func TestParseNarInfoIgnoresUnknownAndBlank(t *testing.T) {
	n, err := ParseNarInfo([]byte("\n" + narInfoText("Future: whatever") + "\n"))
	if err != nil || n.StorePath != testStorePath {
		t.Fatalf("ParseNarInfo = %+v, %v", n, err)
	}
}

func TestParseNarInfoCanonicalizesHashes(t *testing.T) {
	sum := testFileHash()
	forms := []string{sum.String(), "sha256:" + sum.Hex(), "sha256-" + b64(sum)}

	for _, form := range forms {
		text := strings.Replace(narInfoText(), "NarHash: sha256:"+emptySHA256Base32, "NarHash: "+form, 1)

		n, err := ParseNarInfo([]byte(text))
		if err != nil {
			t.Fatalf("NarHash %q: %v", form, err)
		}

		if n.NarHash != sum.String() {
			t.Errorf("NarHash %q parsed to %q, want %q", form, n.NarHash, sum.String())
		}
	}
}

func TestParseNarInfoRejects(t *testing.T) {
	good := narInfoText()

	tests := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"duplicate key", good + "NarSize: 1\n"},
		{"duplicate URL", good + "URL: nar/y.nar\n"},
		{"malformed line", good + "garbage\n"},
		{"bad store path", strings.Replace(good, testStorePath, "/tmp/x", 1)},
		{"store path traversal", strings.Replace(good, testStorePath, StoreDir+"/../etc", 1)},
		{"missing URL", strings.Replace(good, "URL: nar/"+emptySHA256Base32+".nar.xz\n", "", 1)},
		{"empty compression", strings.Replace(good, "Compression: xz", "Compression: ", 1)},
		{"missing NarHash", strings.Replace(good, "NarHash: sha256:"+emptySHA256Base32+"\n", "", 1)},
		{"bad NarHash", strings.Replace(good, "NarHash: sha256:"+emptySHA256Base32, "NarHash: sha256:zzz", 1)},
		{"bad FileHash", strings.Replace(good, "FileHash: sha256:"+emptySHA256Base32, "FileHash: md5:abc", 1)},
		{"negative size", strings.Replace(good, "NarSize: 5678", "NarSize: -1", 1)},
		{"non numeric size", strings.Replace(good, "FileSize: 1234", "FileSize: big", 1)},
		{"bad reference", strings.Replace(good, testRefA, "nothash", 1)},
		{"bad deriver", strings.Replace(good, testHash+"-hello-2.12.1.drv", "../drv", 1)},
		{"signature without colon", strings.Replace(good, "test-1:AAAA", "nocolon", 1)},
		{"too large", good + "X: " + strings.Repeat("a", MaxNarInfoBytes) + "\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseNarInfo([]byte(tc.text)); !errors.Is(err, ErrInvalidNarInfo) {
				t.Fatalf("error = %v, want ErrInvalidNarInfo", err)
			}
		})
	}
}

func TestParseSHA256(t *testing.T) {
	sum := testFileHash()

	valid := []string{sum.String(), "sha256:" + sum.Hex(), "sha256-" + b64(sum)}
	for _, in := range valid {
		got, err := ParseSHA256(in)
		if err != nil || got != sum {
			t.Errorf("ParseSHA256(%q) = %x, %v", in, got, err)
		}
	}

	invalid := []string{
		"", "sha256:", emptySHA256Base32, sum.Hex(), "sha1:" + sum.Hex(), "sha256:" + sum.Hex()[1:],
		"sha256:" + strings.Repeat("e", 52), "sha256-AAAA", "sha256:" + "z" + emptySHA256Base32[1:],
	}
	for _, in := range invalid {
		if _, err := ParseSHA256(in); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("ParseSHA256(%q) error = %v, want ErrInvalidHash", in, err)
		}
	}

	if sum.Base32() != emptySHA256Base32 || len(sum.Hex()) != 64 {
		t.Errorf("Base32/Hex = %q, %q", sum.Base32(), sum.Hex())
	}
}

func TestNarFile(t *testing.T) {
	tests := []struct {
		name string
		url  string
		ok   bool
	}{
		{"bare nar", "nar/" + emptySHA256Base32 + ".nar", true},
		{"xz", "nar/" + emptySHA256Base32 + ".nar.xz", true},
		{"zstd", "nar/" + emptySHA256Base32 + ".nar.zst", true},
		{"bzip2", "nar/" + emptySHA256Base32 + ".nar.bz2", true},
		{"brotli", "nar/" + emptySHA256Base32 + ".nar.br", true},
		{"no nar dir", emptySHA256Base32 + ".nar.xz", false},
		{"other dir", "files/" + emptySHA256Base32 + ".nar.xz", false},
		{"absolute", "/nar/" + emptySHA256Base32 + ".nar.xz", false},
		{"no nar suffix", "nar/" + emptySHA256Base32, false},
		{"upper case ext", "nar/" + emptySHA256Base32 + ".nar.XZ", false},
		{"ext without dot", "nar/" + emptySHA256Base32 + ".narxz", false},
		{"long ext", "nar/" + emptySHA256Base32 + ".nar.abcdefghi", false},
		{"double ext", "nar/" + emptySHA256Base32 + ".nar.x/../y", false},
		{"bad hash", "nar/" + strings.Repeat("e", 52) + ".nar.xz", false},
		{"short hash", "nar/abc.nar.xz", false},
		{"traversal", "nar/../" + emptySHA256Base32 + ".nar", false},
		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, hash, err := (&NarInfo{URL: tc.url}).NarFile()
			if !tc.ok {
				if !errors.Is(err, ErrInvalidNarFile) {
					t.Fatalf("NarFile(%q) error = %v, want ErrInvalidNarFile", tc.url, err)
				}

				return
			}

			if err != nil || file != strings.TrimPrefix(tc.url, "nar/") || hash != testFileHash() {
				t.Fatalf("NarFile(%q) = %q, %x, %v", tc.url, file, hash, err)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	n, err := ParseNarInfo([]byte(narInfoText()))
	if err != nil {
		t.Fatal(err)
	}

	got, err := n.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	want := "1;" + testStorePath + ";sha256:" + emptySHA256Base32 + ";5678;" +
		StoreDir + "/" + testRefA + "," + StoreDir + "/" + testRefB
	if got != want {
		t.Fatalf("Fingerprint =\n%s\nwant\n%s", got, want)
	}

	n.References = nil

	got, err = n.Fingerprint()
	if err != nil || !strings.HasSuffix(got, ";5678;") {
		t.Fatalf("Fingerprint without references = %q, %v", got, err)
	}

	n.NarHash = "bogus"
	if _, err := n.Fingerprint(); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("Fingerprint with a bad hash error = %v, want ErrInvalidHash", err)
	}
}
