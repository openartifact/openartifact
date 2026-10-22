// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import "strings"

// splitStatements splits a SQL script into its statements, one string per
// statement, without the terminating semicolon. Semicolons inside string
// literals (including backslash escapes in E-strings), quoted identifiers, dollar-quoted bodies
// (DO $$ ... $$, CREATE FUNCTION ... $body$ ... $body$) and comments do not
// split. Segments holding only whitespace and comments are dropped. An
// unterminated literal or comment runs to the end of the script and is left
// for the server to reject.
//
// The migrator uses it for .no_tx.sql files: PostgreSQL runs a multi-statement
// simple query inside one implicit transaction, where CREATE INDEX
// CONCURRENTLY (and VACUUM, ...) is refused, so every statement must reach the
// server as its own query.
func splitStatements(script string) []string {
	var (
		stmts    []string
		start    int
		hasToken bool
	)

	flush := func(end int) {
		if hasToken {
			stmts = append(stmts, strings.TrimSpace(script[start:end]))
		}

		hasToken = false
	}

	for i := 0; i < len(script); {
		next, token := scanSQLToken(script, i)
		switch token {
		case tokenTerminator:
			flush(i)

			start = next
		case tokenCode:
			hasToken = true
		case tokenSpace:
		}

		i = next
	}

	flush(len(script))

	return stmts
}

type sqlToken int

const (
	tokenSpace      sqlToken = iota // whitespace or a comment
	tokenCode                       // anything that is part of a statement
	tokenTerminator                 // the ';' ending a statement
)

// scanSQLToken classifies the lexical element starting at script[i] and
// returns the index just past it.
func scanSQLToken(script string, i int) (int, sqlToken) {
	c := script[i]

	switch {
	case c == ';':
		return i + 1, tokenTerminator
	case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
		return i + 1, tokenSpace
	case strings.HasPrefix(script[i:], "--"):
		return skipLineComment(script, i), tokenSpace
	case strings.HasPrefix(script[i:], "/*"):
		return skipBlockComment(script, i), tokenSpace
	case c == '\'':
		return skipQuoted(script, i, '\'', isEscapeStringPrefix(script, i)), tokenCode
	case c == '"':
		return skipQuoted(script, i, '"', false), tokenCode
	case c == '$' && (i == 0 || !isIdentChar(script[i-1])):
		return skipDollarQuoted(script, i), tokenCode
	default:
		return i + 1, tokenCode
	}
}

func skipLineComment(script string, i int) int {
	if nl := strings.IndexByte(script[i:], '\n'); nl >= 0 {
		return i + nl + 1
	}

	return len(script)
}

// skipBlockComment skips a /* */ comment; PostgreSQL allows them to nest.
func skipBlockComment(script string, i int) int {
	depth := 0

	for i < len(script) {
		switch {
		case strings.HasPrefix(script[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(script[i:], "*/"):
			depth--
			i += 2

			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}

	return len(script)
}

// isEscapeStringPrefix reports whether the quote at script[i] opens an E'...'
// string, where a backslash escapes the next character.
func isEscapeStringPrefix(script string, i int) bool {
	if i == 0 || (script[i-1] != 'e' && script[i-1] != 'E') {
		return false
	}

	return i == 1 || !isIdentChar(script[i-2])
}

// skipQuoted skips a '...' literal or "..." identifier starting at script[i];
// a doubled quote is an escaped quote.
func skipQuoted(script string, i int, quote byte, backslash bool) int {
	for i++; i < len(script); i++ {
		switch {
		case backslash && script[i] == '\\':
			i++
		case script[i] == quote:
			if i+1 < len(script) && script[i+1] == quote {
				i++

				continue
			}

			return i + 1
		}
	}

	return len(script)
}

// skipDollarQuoted skips a $tag$ ... $tag$ body starting at script[i]. A '$'
// that does not open a dollar quote (a $1 parameter) is a single character.
func skipDollarQuoted(script string, i int) int {
	end := i + 1
	for end < len(script) && isIdentChar(script[end]) && script[end] != '$' {
		end++
	}

	if end >= len(script) || script[end] != '$' || (end > i+1 && script[i+1] >= '0' && script[i+1] <= '9') {
		return i + 1
	}

	tag := script[i : end+1]
	if closing := strings.Index(script[end+1:], tag); closing >= 0 {
		return end + 1 + closing + len(tag)
	}

	return len(script)
}

// isIdentChar reports whether c can continue an identifier (a dollar sign
// included); bytes >= 0x80 belong to multibyte letters.
func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
