// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ociref_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/ociref"
)

func TestParseCosignTag(t *testing.T) {
	t.Parallel()

	hexA := strings.Repeat("ab", 32)

	tests := []struct {
		name    string
		tag     string
		subject string
		kind    ociref.Kind
		ok      bool
	}{
		{"referrers schema", "sha256-" + hexA, "sha256:" + hexA, ociref.KindReferrersIndex, true},
		{"signature", "sha256-" + hexA + ".sig", "sha256:" + hexA, ociref.KindSignature, true},
		{"attestation", "sha256-" + hexA + ".att", "sha256:" + hexA, ociref.KindAttestation, true},
		{"sbom", "sha256-" + hexA + ".sbom", "sha256:" + hexA, ociref.KindSBOM, true},
		{"source", "sha256-" + hexA + ".src", "sha256:" + hexA, ociref.KindSource, true},
		{"plain tag", "latest", "", "", false},
		{"short digest", "sha256-abcd.sig", "", "", false},
		{"uppercase hex", "sha256-" + strings.ToUpper(hexA) + ".sig", "", "", false},
		{"non hex", "sha256-" + strings.Repeat("zz", 32) + ".sig", "", "", false},
		{"unknown suffix", "sha256-" + hexA + ".txt", "", "", false},
		{"suffix without dot", "sha256-" + hexA + "sig", "", "", false},
		{"too long digest", "sha256-" + hexA + "00", "", "", false},
		{"other algorithm", "sha512-" + hexA + ".sig", "", "", false},
		{"empty", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			subject, kind, ok := ociref.ParseCosignTag(tt.tag)
			if subject != tt.subject || kind != tt.kind || ok != tt.ok {
				t.Fatalf("ParseCosignTag(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.tag, subject, kind, ok, tt.subject, tt.kind, tt.ok)
			}
		})
	}
}

func TestTagPatternMatchesParser(t *testing.T) {
	t.Parallel()

	re := regexp.MustCompile(ociref.TagPattern)
	hexA := strings.Repeat("ab", 32)

	for _, tag := range []string{
		"sha256-" + hexA, "sha256-" + hexA + ".sig", "sha256-" + hexA + ".att", "sha256-" + hexA + ".sbom", "sha256-" + hexA + ".src",
		"latest", "sha256-abcd.sig", "sha256-" + strings.ToUpper(hexA) + ".sig", "sha256-" + hexA + ".txt", "sha256-" + hexA + "sig",
		"sha256-" + hexA + "00", "sha512-" + hexA, "", "xsha256-" + hexA, "sha256-" + hexA + ".sig\n",
	} {
		if _, _, want := ociref.ParseCosignTag(tag); re.MatchString(tag) != want {
			t.Errorf("TagPattern matches %q = %v, ParseCosignTag says %v", tag, re.MatchString(tag), want)
		}
	}
}

func TestCosignTagRoundTrip(t *testing.T) {
	t.Parallel()

	subject := "sha256:" + strings.Repeat("0f", 32)

	for _, kind := range []ociref.Kind{
		ociref.KindReferrersIndex, ociref.KindSignature, ociref.KindAttestation, ociref.KindSBOM, ociref.KindSource,
	} {
		tag := ociref.CosignTag(subject, kind)

		gotSubject, gotKind, ok := ociref.ParseCosignTag(tag)
		if !ok || gotSubject != subject || gotKind != kind {
			t.Errorf("round trip of %q = (%q, %q, %v)", kind, gotSubject, gotKind, ok)
		}
	}

	for _, bad := range []string{"", "sha256:abcd", "sha512:" + strings.Repeat("0f", 32), "sha256:" + strings.Repeat("0F", 32)} {
		if got := ociref.CosignTag(bad, ociref.KindSignature); got != "" {
			t.Errorf("CosignTag(%q) = %q, want empty", bad, got)
		}
	}

	if got := ociref.CosignTag(subject, "bogus"); got != "" {
		t.Errorf("CosignTag with unknown kind = %q, want empty", got)
	}

	if got := len(ociref.CosignTags(subject)); got != 5 {
		t.Errorf("CosignTags returned %d tags, want 5", got)
	}
}

func TestParseQuayExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"30s", 30 * time.Second, true},
		{"90m", 90 * time.Minute, true},
		{"5h", 5 * time.Hour, true},
		{"2d", 48 * time.Hour, true},
		{"1w", 7 * 24 * time.Hour, true},
		{" 3d ", 72 * time.Hour, true},
		{"", 0, false},
		{"d", 0, false},
		{"5", 0, false},
		{"0d", 0, false},
		{"-1d", 0, false},
		{"1.5h", 0, false},
		{"2y", 0, false},
		{"1D", 0, false},
		{"1d2h", 0, false},
		{"99999999999999w", 0, false},
		{"999999999w", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got, ok := ociref.ParseQuayExpiry(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ParseQuayExpiry(%q) = (%v, %v), want (%v, %v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}
