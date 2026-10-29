// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repotype

import "strings"

// PURLEscape percent-encodes one Package URL component: everything but the
// unreserved characters (letters, digits, "." "-" "_" "~"), a ':' included
// ("1:2.0" is "1%3A2.0"). It is the strictest form, which every purl parser
// decodes; the SBOM and the vulnerability side both build their purls with it,
// so the two name a package alike.
func PURLEscape(s string) string {
	const hexdigits = "0123456789ABCDEF"

	var b strings.Builder

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexdigits[c>>4])
			b.WriteByte(hexdigits[c&15])
		}
	}

	return b.String()
}
