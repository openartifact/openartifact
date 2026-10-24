// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package branding implements white-labelling: the product name, logos,
// favicon, primary colour, login message and support/source links, taken from
// the branding.* configuration and overridable at runtime by administrators.
// It validates every input, sanitises uploaded images and renders the pieces
// of the app shell (index.html, web manifest) that carry the brand.
package branding

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultProductName is shown when nothing else is configured.
	DefaultProductName = "OpenArtifact"
	// DefaultSourceURL is where the corresponding source of the running
	// version is offered (AGPL-3.0 section 13) unless the operator points it at
	// their own copy.
	DefaultSourceURL = "https://github.com/openartifact/openartifact"

	maxProductNameRunes = 64
	maxLoginMessageLen  = 2000
	maxURLLen           = 2048
)

// ErrInvalid wraps every validation failure; its message is safe to show to
// the administrator who supplied the value.
var ErrInvalid = errors.New("invalid branding value")

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// ValidateProductName checks and normalises a product name: 1 to 64
// characters after trimming, no control or format characters.
func ValidateProductName(s string) (string, error) {
	s = strings.TrimSpace(s)

	n := utf8.RuneCountInString(s)
	if n == 0 {
		return "", invalidf("product name must not be empty")
	}

	if n > maxProductNameRunes {
		return "", invalidf("product name must be at most %d characters", maxProductNameRunes)
	}

	if !utf8.ValidString(s) {
		return "", invalidf("product name must be valid UTF-8")
	}

	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return "", invalidf("product name must not contain control characters")
		}
	}

	return s, nil
}

// ValidateColor checks a #rrggbb colour and returns it lower-cased. The empty
// string is valid and means "built-in colour".
func ValidateColor(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}

	if !colorRe.MatchString(s) {
		return "", invalidf("primary colour must be written as #rrggbb")
	}

	return strings.ToLower(s), nil
}

// ValidateLoginMessage checks the login message: at most 2000 bytes of valid
// UTF-8 without control characters other than newline and tab. Markdown is
// not interpreted here; the web UI renders a tiny subset without innerHTML.
func ValidateLoginMessage(s string) (string, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")

	if len(s) > maxLoginMessageLen {
		return "", invalidf("login message must be at most %d bytes", maxLoginMessageLen)
	}

	if !utf8.ValidString(s) {
		return "", invalidf("login message must be valid UTF-8")
	}

	for _, r := range s {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return "", invalidf("login message must not contain control characters")
		}
	}

	return s, nil
}

// ValidateLinkURL checks a link target. Only http and https URLs with a host
// and no credentials are accepted, plus mailto: when allowMailto is set. The
// empty string is valid and means "no link".
func ValidateLinkURL(s string, allowMailto bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}

	if len(s) > maxURLLen {
		return "", invalidf("URL must be at most %d bytes", maxURLLen)
	}

	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", invalidf("URL must not contain spaces or control characters")
		}
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", invalidf("URL is not valid")
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" || u.Hostname() == "" {
			return "", invalidf("URL needs a host")
		}

		if u.User != nil {
			return "", invalidf("URL must not contain credentials")
		}
	case "mailto":
		if !allowMailto || u.Opaque == "" {
			return "", invalidf("URL must start with http:// or https://")
		}
	default:
		if allowMailto {
			return "", invalidf("URL must start with http://, https:// or mailto:")
		}

		return "", invalidf("URL must start with http:// or https://")
	}

	return s, nil
}

// ValidateSupportURL checks the support link (http, https or mailto; empty
// hides the link).
func ValidateSupportURL(s string) (string, error) { return ValidateLinkURL(s, true) }

// ValidateSourceURL checks the source-code link. It can never be empty: the
// AGPL requires the running version's source to be offered to every user. An
// empty value therefore means the default.
func ValidateSourceURL(s string) (string, error) {
	s, err := ValidateLinkURL(s, false)
	if err != nil {
		return "", err
	}

	if s == "" {
		return DefaultSourceURL, nil
	}

	return s, nil
}
