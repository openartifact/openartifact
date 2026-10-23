// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import "strings"

// maxLicenseFieldLen bounds a "License:" value that is treated as a license
// name; longer values are the license text and cannot name a license.
const maxLicenseFieldLen = 100

// licenseClassifierPrefix starts a trove license classifier.
const licenseClassifierPrefix = "License ::"

// parsePyPI reads the licenses of a METADATA / PKG-INFO header block in order
// of authority:
//
//  1. License-Expression (PEP 639): an SPDX expression;
//  2. License: a short, single-line value (a name or an expression; anything
//     longer is the license text and says nothing a policy could match);
//  3. "License ::" trove classifiers.
//
// The first step that yields something wins; the others are not merged, as a
// package that has an expression no longer uses the older fields.
func parsePyPI(text string) Result {
	h := parseHeaders(text)

	if expr := first(h["license-expression"]); expr != "" {
		return Result{Licenses: []string{expr}, Source: "License-Expression"}
	}

	for _, v := range h["license"] {
		if v = strings.TrimSpace(v); v != "" && len(v) <= maxLicenseFieldLen &&
			!strings.ContainsAny(v, "\r\n") && !strings.EqualFold(v, "UNKNOWN") {
			return Result{Licenses: []string{v}, Source: "License"}
		}
	}

	var out []string

	for _, c := range h["classifier"] {
		if licenseClassifier(c) {
			out = append(out, strings.TrimSpace(c))
		}
	}

	if len(out) > 0 {
		return Result{Licenses: out, Source: "classifier"}
	}

	return Result{}
}

// licenseClassifier reports whether c is a license classifier that names a
// license: "License :: OSI Approved" alone does not.
func licenseClassifier(c string) bool {
	c = strings.TrimSpace(c)

	return strings.HasPrefix(c, licenseClassifierPrefix) && strings.Count(c, "::") >= 2
}

func first(values []string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}

	return ""
}

// parseHeaders parses the RFC 822 header block at the start of text into
// lowercase field name -> values. Continuation lines (leading space or tab)
// join the previous value with a newline; the block ends at the first blank
// line.
func parseHeaders(text string) map[string][]string {
	headers := make(map[string][]string)

	var key string

	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")

		if line == "" {
			break
		}

		if line[0] == ' ' || line[0] == '\t' {
			if vals := headers[key]; len(vals) > 0 {
				vals[len(vals)-1] += "\n" + strings.TrimSpace(line)
			}

			continue
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok {
			key = ""
			continue
		}

		key = strings.ToLower(strings.TrimSpace(name))
		headers[key] = append(headers[key], strings.TrimSpace(value))
	}

	return headers
}
