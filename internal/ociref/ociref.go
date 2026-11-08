// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ociref recognises the OCI reference conventions that attach
// artifacts to an image through tags instead of the Referrers API: cosign's
// "sha256-<hex>.sig|.att|.sbom|.src" tags and the referrers tag schema
// ("sha256-<hex>"), plus Quay's "quay.expires-after" duration grammar.
package ociref

import (
	"math"
	"slices"
	"strings"
	"time"
)

// Kind says what a dependent tag holds for its subject image.
type Kind string

const (
	// KindReferrersIndex is the referrers tag schema fallback: the tag
	// "sha256-<hex>" holds an image index listing the referrers of the subject.
	KindReferrersIndex Kind = "referrers-index"
	// KindSignature is a cosign signature (".sig").
	KindSignature Kind = "signature"
	// KindAttestation is a cosign attestation (".att").
	KindAttestation Kind = "attestation"
	// KindSBOM is a cosign SBOM (".sbom").
	KindSBOM Kind = "sbom"
	// KindSource is a cosign source attachment (".src").
	KindSource Kind = "source"
)

const (
	digestAlgo = "sha256"
	hexLen     = 64
	// tagPrefix is the digest with its colon replaced, as tags cannot hold one.
	tagPrefix = digestAlgo + "-"
)

// TagPattern is the regular expression a dependent tag matches, for the SQL
// side (a Postgres `~`) that cannot call ParseCosignTag. The subject digest is
// "sha256:" followed by the 64 characters after the tag's first 7, so a query
// can derive it with substr(name, 8, 64). Keep it in step with ParseCosignTag
// (TestTagPatternMatchesParser).
const TagPattern = `^sha256-[0-9a-f]{64}(\.(sig|att|sbom|src))?$`

// SubjectSQL is the SQL expression of the subject digest of a dependent tag
// column, valid for names that match TagPattern.
func SubjectSQL(nameColumn string) string {
	return "'sha256:' || substr(" + nameColumn + ", 8, 64)"
}

// cosignKinds are the kinds cosign stores under a tag suffix, in listing order.
func cosignKinds() []Kind {
	return []Kind{KindSignature, KindAttestation, KindSBOM, KindSource}
}

// suffix is the tag suffix of a cosign kind ("" for any other kind).
func (k Kind) suffix() string {
	switch k {
	case KindSignature:
		return ".sig"
	case KindAttestation:
		return ".att"
	case KindSBOM:
		return ".sbom"
	case KindSource:
		return ".src"
	case KindReferrersIndex:
		return ""
	}

	return ""
}

// ParseCosignTag reports whether tag is a dependent tag, and if so the digest
// ("sha256:<hex>") of the subject image it belongs to and what it holds.
func ParseCosignTag(tag string) (string, Kind, bool) {
	rest, ok := strings.CutPrefix(tag, tagPrefix)
	if !ok || len(rest) < hexLen || !isLowerHex(rest[:hexLen]) {
		return "", "", false
	}

	suffix := rest[hexLen:]
	subject := digestAlgo + ":" + rest[:hexLen]

	if suffix == "" {
		return subject, KindReferrersIndex, true
	}

	for _, k := range cosignKinds() {
		if suffix == k.suffix() {
			return subject, k, true
		}
	}

	return "", "", false
}

// CosignTag is the inverse of ParseCosignTag: the tag holding kind for the
// subject digest "sha256:<hex>". It is empty for a malformed subject or an
// unknown kind.
func CosignTag(subject string, kind Kind) string {
	hexPart, ok := strings.CutPrefix(subject, digestAlgo+":")
	if !ok || len(hexPart) != hexLen || !isLowerHex(hexPart) {
		return ""
	}

	if kind == KindReferrersIndex {
		return tagPrefix + hexPart
	}

	if slices.Contains(cosignKinds(), kind) {
		return tagPrefix + hexPart + kind.suffix()
	}

	return ""
}

// CosignTags lists the tags that can hold dependents of the subject digest, in
// a fixed order: the cosign kinds, then the referrers tag schema.
func CosignTags(subject string) []string {
	kinds := cosignKinds()
	tags := make([]string, 0, len(kinds)+1)

	for _, k := range kinds {
		if t := CosignTag(subject, k); t != "" {
			tags = append(tags, t)
		}
	}

	if t := CosignTag(subject, KindReferrersIndex); t != "" {
		tags = append(tags, t)
	}

	return tags
}

func isLowerHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// maxExpiryDigits bounds the number so the multiplication below cannot
// overflow before the explicit range check.
const maxExpiryDigits = 9

// expiryUnit is the duration of a unit letter of a quay.expires-after value.
func expiryUnit(c byte) (time.Duration, bool) {
	switch c {
	case 's':
		return time.Second, true
	case 'm':
		return time.Minute, true
	case 'h':
		return time.Hour, true
	case 'd':
		return 24 * time.Hour, true
	case 'w':
		return 7 * 24 * time.Hour, true
	}

	return 0, false
}

// ParseQuayExpiry parses a Quay "quay.expires-after" value: digits and one
// unit of s, m, h, d or w ("90m", "2w"). Zero, a missing number or unit, other
// units and values too large for a time.Duration give false.
func ParseQuayExpiry(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || len(s) > maxExpiryDigits+1 {
		return 0, false
	}

	unit, ok := expiryUnit(s[len(s)-1])
	if !ok {
		return 0, false
	}

	var n int64

	for _, c := range s[:len(s)-1] {
		if c < '0' || c > '9' {
			return 0, false
		}

		n = n*10 + int64(c-'0')
	}

	if n == 0 || n > math.MaxInt64/int64(unit) {
		return 0, false
	}

	return time.Duration(n) * unit, true
}
