// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"strings"
	"unicode"
)

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

	p := spdxParser{tokens: tokens, strict: true}
	node := p.expression()

	return node != nil && p.pos == len(p.tokens)
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
		case '(', ')':
			flush()
			tokens = append(tokens, string(r))
		default:
			if unicode.IsSpace(r) {
				flush()

				continue
			}

			cur.WriteRune(r)
		}
	}

	flush()

	return tokens
}

// spdxNode is a parsed SPDX expression: a license id (leaf) or an AND / OR
// of sub-expressions. An exception after WITH is dropped from the tree.
type spdxNode struct {
	id   string // normalized id of a leaf; empty for AND / OR nodes
	and  bool   // for inner nodes: AND (all children) when true, OR (any child) when false
	kids []*spdxNode
}

// allowed reports whether the expression can be satisfied with allowed
// licenses only: a leaf needs its id allowed, OR needs one allowed branch,
// AND needs every branch allowed.
func (n *spdxNode) allowed(set map[string]struct{}) bool {
	if len(n.kids) == 0 {
		_, ok := set[n.id]

		return ok
	}

	for _, k := range n.kids {
		ok := k.allowed(set)
		if n.and && !ok {
			return false
		}

		if !n.and && ok {
			return true
		}
	}

	return n.and
}

// spdxParser is a recursive-descent parser for
//
//	expression = and { "OR" and }
//	and        = term { "AND" term }
//	term       = "(" expression ")" | id [ "WITH" exception ]
//
// AND binds tighter than OR, as in the SPDX specification. A strict parser
// accepts only identifiers of the SPDX License List; a lenient one accepts any
// word as an id, for policy evaluation of free-form metadata.
type spdxParser struct {
	tokens []string
	pos    int
	depth  int // open parentheses
	strict bool
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

func (p *spdxParser) expression() *spdxNode { return p.chain("OR", false, p.and) }

func (p *spdxParser) and() *spdxNode {
	return p.chain("AND", true, p.term)
}

func (p *spdxParser) chain(op string, isAnd bool, operand func() *spdxNode) *spdxNode {
	first := operand()
	if first == nil {
		return nil
	}

	kids := []*spdxNode{first}

	for strings.EqualFold(p.peek(), op) {
		p.pos++

		next := operand()
		if next == nil {
			return nil
		}

		kids = append(kids, next)
	}

	if len(kids) == 1 {
		return first
	}

	return &spdxNode{and: isAnd, kids: kids}
}

func (p *spdxParser) term() *spdxNode {
	tok := p.peek()

	if tok == "(" {
		// A license string is uploaded metadata: without a cap, a few MiB of "("
		// recurse past the 1 GB goroutine stack limit, which kills the process.
		if p.depth >= maxSPDXDepth {
			return nil
		}

		p.pos++
		p.depth++

		n := p.expression()
		if n == nil || p.peek() != ")" {
			return nil
		}

		p.pos++
		p.depth--

		return n
	}

	if tok == "" || tok == ")" {
		return nil
	}

	if p.strict {
		if _, ok := SPDXLicenseID(strings.TrimSuffix(tok, "+")); !ok {
			return nil
		}
	} else if isSPDXOperator(tok) {
		return nil
	}

	p.pos++

	if strings.EqualFold(p.peek(), "WITH") {
		p.pos++

		exc := p.peek()
		if exc == "" || exc == "(" || exc == ")" {
			return nil
		}

		if _, ok := SPDXExceptionID(exc); !ok && p.strict {
			return nil
		}

		p.pos++
	}

	return &spdxNode{id: normalizeID(tok)}
}

func isSPDXOperator(tok string) bool {
	switch strings.ToUpper(tok) {
	case "AND", "OR", "WITH":
		return true
	}

	return false
}

// parseSPDXLenient parses expr into a tree without requiring its identifiers to
// be on the SPDX License List. It returns nil when expr is not a well-formed
// expression ("Apache License 2.0", unbalanced parentheses).
func parseSPDXLenient(expr string) *spdxNode {
	p := spdxParser{tokens: tokenizeSPDX(expr)}

	n := p.expression()
	if n == nil || p.pos != len(p.tokens) {
		return nil
	}

	return n
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
