// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"slices"
	"strings"
)

// The relations of the Link headers a registry sends.
const (
	RelCanonical   = "canonical"
	RelAlternate   = "alternate"
	RelLatest      = "latest-version"
	RelSuccessor   = "successor-version"
	RelPredecessor = "predecessor-version"
)

// Link is one link of a Link header (RFC 8288): a target, its relation and the
// other parameters ("filename", "swift-tools-version").
type Link struct {
	URL    string
	Rel    string
	Params map[string]string
}

// String renders the link as a Link header value, parameters in name order.
func (l Link) String() string {
	var b strings.Builder

	b.WriteString("<" + l.URL + `>; rel="` + quote(l.Rel) + `"`)

	keys := make([]string, 0, len(l.Params))
	for k := range l.Params {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	for _, k := range keys {
		b.WriteString("; " + k + `="` + quote(l.Params[k]) + `"`)
	}

	return b.String()
}

// quote escapes what a quoted-string cannot hold.
func quote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(s)
}

// ParseLinks parses Link header values. It is tolerant: a link it cannot read
// is skipped, and a parameter name is lower-cased. Only the first "rel" of a
// link counts.
func ParseLinks(values []string) []Link {
	var links []Link

	for _, v := range values {
		for v = strings.TrimSpace(v); v != ""; {
			var l Link

			var ok bool

			l, v, ok = parseLink(v)
			if ok && l.Rel != "" {
				links = append(links, l)
			}
		}
	}

	return links
}

// parseLink reads one "<url>; name=value; ..." element from the front of s and
// returns the rest after its separating comma. ok is false for an element with
// no "<...>" target; s always shrinks.
func parseLink(s string) (Link, string, bool) {
	if s[0] != '<' {
		// Not a link: skip to the next element.
		_, after, found := strings.Cut(s, ",")
		if !found {
			return Link{}, "", false
		}

		return Link{}, strings.TrimSpace(after), false
	}

	end := strings.IndexByte(s, '>')
	if end < 0 {
		return Link{}, "", false
	}

	l := Link{URL: s[1:end], Params: map[string]string{}}
	s = s[end+1:]

	for {
		s = strings.TrimSpace(s)

		switch {
		case s == "":
			return l, "", true
		case s[0] == ',':
			return l, strings.TrimSpace(s[1:]), true
		case s[0] != ';':
			// Garbage after the target: drop the element.
			_, after, found := strings.Cut(s, ",")
			if !found {
				return Link{}, "", false
			}

			return Link{}, strings.TrimSpace(after), false
		}

		var name, value string

		name, value, s = parseParam(s[1:])
		if name == "" {
			continue
		}

		if name == "rel" {
			if l.Rel == "" {
				l.Rel = strings.ToLower(value)
			}

			continue
		}

		l.Params[name] = value
	}
}

// parseParam reads "name=value" (value a token or a quoted string) and returns
// what follows it, up to the next ";" or ",".
func parseParam(s string) (string, string, string) {
	s = strings.TrimLeft(s, " \t")

	i := strings.IndexAny(s, "=;,")
	if i < 0 {
		return "", "", ""
	}

	if s[i] != '=' {
		return "", "", s[i:]
	}

	name := strings.ToLower(strings.TrimSpace(s[:i]))
	s = strings.TrimLeft(s[i+1:], " \t")

	if s != "" && s[0] == '"' {
		var b strings.Builder

		for i := 1; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\\' && i+1 < len(s):
				i++
				b.WriteByte(s[i])
			case c == '"':
				return name, b.String(), s[i+1:]
			default:
				b.WriteByte(c)
			}
		}

		return name, b.String(), ""
	}

	i = strings.IndexAny(s, ";,")
	if i < 0 {
		return name, strings.TrimSpace(s), ""
	}

	return name, strings.TrimSpace(s[:i]), s[i:]
}
