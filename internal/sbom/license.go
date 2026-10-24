// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strings"

	"github.com/openartifact/openartifact/internal/license"
)

// licenseEntry is one license of a package: either a valid SPDX license
// expression (Expr) or a free-form name that is not an SPDX identifier (Name).
type licenseEntry struct {
	Expr string
	Name string
}

// simple reports whether the entry is a single SPDX license id (no operators).
func (e licenseEntry) simple() bool {
	return e.Expr != "" && !strings.ContainsAny(e.Expr, " ()")
}

// classifyLicenses turns raw license strings (stored scan results, artifact
// properties) into entries: an SPDX id or expression, canonically cased, when
// the string parses as one, else a name. Duplicates are dropped.
func classifyLicenses(raw ...[]string) []licenseEntry {
	det := license.NewDetector(nil)
	seen := map[string]struct{}{}

	var out []licenseEntry

	for _, list := range raw {
		for _, r := range list {
			r = strings.TrimSpace(r)
			if r == "" || strings.EqualFold(r, "NOASSERTION") || strings.EqualFold(r, "NONE") {
				continue
			}

			e := licenseEntry{Name: r}
			if expr, ok := canonicalSPDXExpression(r); ok {
				e = licenseEntry{Expr: expr}
			} else if expr, ok := canonicalSPDXExpression(det.NormalizeLicense(r)); ok {
				e = licenseEntry{Expr: expr}
			}

			key := e.Expr + "\x00" + e.Name
			if _, dup := seen[key]; dup {
				continue
			}

			seen[key] = struct{}{}

			out = append(out, e)
		}
	}

	return out
}

// canonicalSPDXExpression validates expr against the SPDX expression grammar
// (ids from the SPDX license list, "+" suffix, WITH exception, AND/OR,
// parentheses) and returns it with canonical id casing and normalized
// spacing. ok is false when anything in it is not a known SPDX identifier.
func canonicalSPDXExpression(expr string) (string, bool) {
	p := &exprParser{toks: tokenizeExpr(expr)}
	if len(p.toks) == 0 {
		return "", false
	}

	var b strings.Builder
	if !p.parseOr(&b) || p.pos != len(p.toks) {
		return "", false
	}

	return b.String(), true
}

func tokenizeExpr(s string) []string {
	var (
		toks []string
		cur  strings.Builder
	)

	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}

	for _, r := range s {
		switch r {
		case '(', ')':
			flush()

			toks = append(toks, string(r))
		case ' ', '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}

	flush()

	return toks
}

// exprParser is a recursive-descent parser: or := and ("OR" and)*,
// and := atom ("AND" atom)*, atom := "(" or ")" | id ["+"] ["WITH" exception].
type exprParser struct {
	toks  []string
	pos   int
	depth int // open parentheses
}

// maxExprDepth bounds the parenthesis nesting of an expression; real ones
// nest a few levels.
const maxExprDepth = 64

func (p *exprParser) peekOp(op string) bool {
	return p.pos < len(p.toks) && strings.EqualFold(p.toks[p.pos], op)
}

func (p *exprParser) parseOr(b *strings.Builder) bool {
	if !p.parseAnd(b) {
		return false
	}

	for p.peekOp("OR") {
		p.pos++

		b.WriteString(" OR ")

		if !p.parseAnd(b) {
			return false
		}
	}

	return true
}

func (p *exprParser) parseAnd(b *strings.Builder) bool {
	if !p.parseAtom(b) {
		return false
	}

	for p.peekOp("AND") {
		p.pos++

		b.WriteString(" AND ")

		if !p.parseAtom(b) {
			return false
		}
	}

	return true
}

func (p *exprParser) parseAtom(b *strings.Builder) bool {
	if p.pos >= len(p.toks) {
		return false
	}

	tok := p.toks[p.pos]

	if tok == "(" {
		// A license string is uploaded metadata: without a cap, a few MiB of "("
		// recurse past the 1 GB goroutine stack limit, which kills the process.
		if p.depth >= maxExprDepth {
			return false
		}

		p.pos++
		p.depth++

		b.WriteByte('(')

		if !p.parseOr(b) || p.pos >= len(p.toks) || p.toks[p.pos] != ")" {
			return false
		}

		p.pos++
		p.depth--

		b.WriteByte(')')

		return true
	}

	plus := strings.HasSuffix(tok, "+")

	canon, ok := spdxLicenseSet[strings.ToLower(strings.TrimSuffix(tok, "+"))]
	if !ok {
		return false
	}

	p.pos++

	b.WriteString(canon)

	if plus {
		b.WriteByte('+')
	}

	if p.peekOp("WITH") {
		p.pos++

		if p.pos >= len(p.toks) {
			return false
		}

		exc, ok := spdxExceptionSet[strings.ToLower(p.toks[p.pos])]
		if !ok {
			return false
		}

		p.pos++

		b.WriteString(" WITH " + exc)
	}

	return true
}

// joinExpressions combines several valid expressions into one conjunction
// (every listed license applies), parenthesizing compound ones.
func joinExpressions(exprs []string) string {
	if len(exprs) == 1 {
		return exprs[0]
	}

	parts := make([]string, len(exprs))

	for i, e := range exprs {
		if strings.ContainsAny(e, " ") {
			e = "(" + e + ")"
		}

		parts[i] = e
	}

	return strings.Join(parts, " AND ")
}
