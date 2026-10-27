// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of the values that end up in an index line.
const (
	maxTextLen       = 1024
	maxTokenLen      = 256
	maxTokens        = 2000
	maxProviderPrio  = math.MaxInt32
	dataHashLen      = 64
	checksumPrefix   = "Q1"
	sha1Base64Length = 28
)

// errInvalidField is wrapped by every Package.validate error.
var errInvalidField = errors.New("invalid field")

func invalidField(field, format string, args ...any) error {
	return fmt.Errorf("%w %s: %s", errInvalidField, field, fmt.Sprintf(format, args...))
}

// parsePKGINFO parses the .PKGINFO text of a package: "key = value" lines,
// with '#' comments and blank lines ignored. Unknown keys are ignored; the
// repeatable keys are depend and provides, every other indexed key may appear
// once. The values are not validated here, see Package.validate.
func parsePKGINFO(data []byte) (*Package, error) {
	var (
		p    Package
		seen = make(map[string]bool, 16)
	)

	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, bad("malformed %s line %q", pkginfoName, short(line))
		}

		key, val = strings.TrimSpace(key), strings.TrimSpace(val)

		switch key {
		case "depend":
			if err := appendTokens(&p.Depends, key, val); err != nil {
				return nil, bad("%v", err)
			}

			continue
		case "provides":
			if err := appendTokens(&p.Provides, key, val); err != nil {
				return nil, bad("%v", err)
			}

			continue
		}

		known, err := p.setSingular(key, val)
		if err != nil {
			return nil, bad("%v", err)
		}

		if !known {
			continue
		}

		if seen[key] {
			return nil, bad("duplicate %s in %s", key, pkginfoName)
		}

		seen[key] = true
	}

	for _, key := range [...]string{"pkgname", "pkgver", "arch", "datahash"} {
		if !seen[key] {
			return nil, bad("%s has no %s", pkginfoName, key)
		}
	}

	return &p, nil
}

// appendTokens splits a repeatable key's value on whitespace into dst. A value
// without any token is refused: it would be an empty dependency.
func appendTokens(dst *[]string, key, val string) error {
	if !cleanText(val) {
		return invalidField(key, "invalid characters")
	}

	tokens := strings.Fields(val)
	if len(tokens) == 0 {
		return invalidField(key, "empty value")
	}

	*dst = append(*dst, tokens...)

	return nil
}

// setSingular stores the value of a key that may appear once and reports
// whether the key is one of them.
func (p *Package) setSingular(key, val string) (bool, error) {
	switch key {
	case "pkgname":
		p.Name = val
	case "pkgver":
		p.Version = val
	case "arch":
		p.Arch = val
	case "datahash":
		p.DataHash = strings.ToLower(val)
	case "pkgdesc":
		p.Description = val
	case "url":
		p.URL = val
	case "license":
		p.License = val
	case "origin":
		p.Origin = val
	case "maintainer":
		p.Maintainer = val
	case "commit":
		p.Commit = val
	case "install_if":
		if err := appendTokens(&p.InstallIf, key, val); err != nil {
			return true, err
		}
	case "builddate", "size", "provider_priority":
		return true, p.setNumber(key, val)
	default:
		return false, nil
	}

	return true, nil
}

func (p *Package) setNumber(key, val string) error {
	n, ok := parseCount(val)
	if !ok {
		return invalidField(key, "not a non-negative integer")
	}

	switch key {
	case "builddate":
		p.BuildTime = n
	case "size":
		p.InstalledSize = n
	default:
		if n > maxProviderPrio {
			return invalidField(key, "exceeds %d", maxProviderPrio)
		}

		p.ProviderPriority = int(n)
	}

	return nil
}

// parseCount parses a non-negative decimal int64: digits only, no sign.
func parseCount(s string) (int64, bool) {
	if s == "" || len(s) > 19 {
		return 0, false
	}

	for i := range len(s) {
		if !isDigit(s[i]) {
			return 0, false
		}
	}

	n, err := strconv.ParseInt(s, 10, 64)

	return n, err == nil
}

// cleanText reports whether s is valid UTF-8 without control characters
// (C0, DEL and C1): the values that go on one index line.
func cleanText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	return strings.IndexFunc(s, unicode.IsControl) < 0
}

// validate checks every field that goes into an index line, so that a Package
// that passes cannot inject lines or fields into an APKINDEX and survives a
// render/parse round trip. DataHash is checked when set.
func (p *Package) validate() error {
	if err := p.validateIdentity(); err != nil {
		return err
	}

	for _, f := range [...]struct{ name, val string }{
		{"pkgdesc", p.Description}, {"url", p.URL}, {"license", p.License},
		{"origin", p.Origin}, {"maintainer", p.Maintainer}, {"commit", p.Commit},
	} {
		if len(f.val) > maxTextLen || !cleanText(f.val) {
			return invalidField(f.name, "longer than %d bytes or with control characters", maxTextLen)
		}
	}

	for _, f := range [...]struct {
		name string
		val  []string
	}{{"depend", p.Depends}, {"provides", p.Provides}, {"install_if", p.InstallIf}} {
		if err := validateTokens(f.name, f.val); err != nil {
			return err
		}
	}

	if p.BuildTime < 0 || p.InstalledSize < 0 || p.Size < 0 {
		return invalidField("builddate/size", "negative")
	}

	if p.ProviderPriority < 0 || p.ProviderPriority > maxProviderPrio {
		return invalidField("provider_priority", "out of range")
	}

	return nil
}

func (p *Package) validateIdentity() error {
	switch {
	case !validName(p.Name):
		return invalidField("pkgname", "%q is not a valid package name", short(p.Name))
	case !validVersion(p.Version):
		return invalidField("pkgver", "%q is not a valid version", short(p.Version))
	case !validArch(p.Arch):
		return invalidField("arch", "%q is not a valid architecture", short(p.Arch))
	case p.DataHash != "" && !validHex(p.DataHash, dataHashLen):
		return invalidField("datahash", "not a SHA-256 in hex")
	case !validChecksum(p.Checksum):
		return invalidField("checksum", "%q is not Q1 + base64 SHA-1", short(p.Checksum))
	}

	return nil
}

// validateTokens checks a dependency-like list: each token a non-empty run of
// at most maxTokenLen bytes without whitespace or control characters.
func validateTokens(field string, tokens []string) error {
	if len(tokens) > maxTokens {
		return invalidField(field, "more than %d entries", maxTokens)
	}

	for _, t := range tokens {
		if t == "" || len(t) > maxTokenLen || !cleanText(t) || strings.IndexFunc(t, unicode.IsSpace) >= 0 {
			return invalidField(field, "entry %q is empty, too long or has whitespace", short(t))
		}
	}

	return nil
}

// validHex reports whether s is n lowercase hexadecimal digits.
func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if !isDigit(c) && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// validChecksum reports whether s is "Q1" followed by the base64 of a SHA-1.
func validChecksum(s string) bool {
	rest, ok := strings.CutPrefix(s, checksumPrefix)
	if !ok || len(rest) != sha1Base64Length {
		return false
	}

	_, err := base64.StdEncoding.DecodeString(rest)

	return err == nil
}
