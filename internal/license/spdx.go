// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"strings"
	"sync"
)

// spdxSets holds the lowercase license and exception identifiers.
type spdxSets struct {
	licenses   map[string]struct{}
	exceptions map[string]struct{}
}

//nolint:gochecknoglobals // immutable lookup built once
var spdxTable = sync.OnceValue(func() spdxSets {
	set := func(list string) map[string]struct{} {
		fields := strings.Fields(list)
		m := make(map[string]struct{}, len(fields))

		for _, id := range fields {
			m[strings.ToLower(id)] = struct{}{}
		}

		return m
	}

	return spdxSets{licenses: set(spdxLicenseList), exceptions: set(spdxExceptionList)}
})

// IsSPDXExpression reports whether expr is a well-formed SPDX license
// expression made only of identifiers of the SPDX License List: ids with an
// optional "+", joined by AND / OR, "WITH" a listed exception, with balanced
// parentheses. Identifiers match case-insensitively. Free text that merely
// looks like a license name ("BSD", "BSD License", "Proprietary") is not one,
// which is how an artifact's raw license strings are told apart from resolved
// ones.
func IsSPDXExpression(expr string) bool {
	tokens := tokenizeSPDX(expr)
	if len(tokens) == 0 {
		return false
	}

	p := spdxParser{tokens: tokens, table: spdxTable()}

	return p.expression() && p.pos == len(p.tokens)
}

func tokenizeSPDX(expr string) []string {
	var tokens []string

	var cur strings.Builder

	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}

	for _, r := range expr {
		switch r {
		case ' ', '\t', '\r', '\n':
			flush()
		case '(', ')':
			flush()
			tokens = append(tokens, string(r))
		default:
			cur.WriteRune(r)
		}
	}

	flush()

	return tokens
}

// spdxParser is a recursive-descent parser for
//
//	expression = term { ("AND" | "OR") term }
//	term       = "(" expression ")" | id [ "WITH" exception ]
type spdxParser struct {
	tokens []string
	pos    int
	depth  int // open parentheses
	table  spdxSets
}

// maxSPDXDepth bounds the parenthesis nesting of an expression; real ones
// nest a few levels.
const maxSPDXDepth = 64

func (p *spdxParser) peek() string {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}

	return ""
}

func (p *spdxParser) expression() bool {
	if !p.term() {
		return false
	}

	for {
		switch strings.ToUpper(p.peek()) {
		case "AND", "OR":
			p.pos++

			if !p.term() {
				return false
			}
		default:
			return true
		}
	}
}

func (p *spdxParser) term() bool {
	tok := p.peek()

	if tok == "(" {
		// A license string is uploaded metadata: without a cap, a few MiB of "("
		// recurse past the 1 GB goroutine stack limit, which kills the process.
		if p.depth >= maxSPDXDepth {
			return false
		}

		p.pos++
		p.depth++

		if !p.expression() || p.peek() != ")" {
			return false
		}

		p.pos++
		p.depth--

		return true
	}

	if tok == "" || tok == ")" {
		return false
	}

	id := strings.TrimSuffix(strings.ToLower(tok), "+")
	if _, ok := p.table.licenses[id]; !ok {
		return false
	}

	p.pos++

	if strings.EqualFold(p.peek(), "WITH") {
		p.pos++

		exc := strings.ToLower(p.peek())
		if _, ok := p.table.exceptions[exc]; !ok {
			return false
		}

		p.pos++
	}

	return true
}

// Unresolved returns the licenses that are not SPDX expressions after
// normalization: raw text from an artifact that names no single license
// ("BSD", "BSD License") or one this table does not know. They are kept as
// written and reported so a human can review them.
func (d *Detector) Unresolved(licenses []string) []string {
	out := []string{}

	for _, l := range licenses {
		if n := d.NormalizeLicense(l); n != "" && !IsSPDXExpression(n) && !contains(out, n) {
			out = append(out, n)
		}
	}

	return out
}
