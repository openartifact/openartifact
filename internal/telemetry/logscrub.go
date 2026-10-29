// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"go.uber.org/zap/zapcore"
)

// redacted replaces what must not leave the process in an exported log record.
const redacted = "[redacted]"

// Keys whose value is a request path or a URL, and keys that hold a query
// string. Matching is exact on the zap field key, the names the code base logs.
var (
	urlKeys = map[string]bool{ //nolint:gochecknoglobals // constant set
		"path": true, "url": true, "uri": true, "request_uri": true, "upstream_url": true,
		"upstream": true, "endpoint": true, "href": true, "redirect": true, "location": true,
		"referer": true, "referrer": true,
	}
	queryKeys = map[string]bool{ //nolint:gochecknoglobals // constant set
		"query": true, "raw_query": true, "rawquery": true, "query_string": true,
	}
)

var (
	// urlRe finds absolute URLs inside free text (an error message quoting the
	// upstream request).
	urlRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>` + "`" + `]+`)
	// ticketRe is a signed download ticket: a base64url payload and a base64url
	// HMAC-SHA256 signature (43 characters) joined by a dot, the form of the
	// Terraform provider download ticket; a JWT also matches. The long second
	// part keeps host names like "a-long-name.another-long-name" out of it.
	ticketRe = regexp.MustCompile(`[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{40,}(?:\.[A-Za-z0-9_-]{16,})?`)
	// pathQueryRe is the query string of a path quoted in free text.
	pathQueryRe = regexp.MustCompile(`(/[^\s?"'<>]*)\?[^\s"'<>]*`)
)

// minTicketLen is the shortest string ticketRe can match (16 + 1 + 40).
const minTicketLen = 57

// scrubCore wraps the core that exports log records and rewrites every field
// that can carry a secret before the inner core sees it: URLs lose their
// userinfo, query and fragment, request paths lose download tickets and query
// strings, query-string fields are replaced, and error text is cleaned the
// same way. The stdout/file encoders are not behind it: the log line stays as
// written, only what leaves the process over OTLP is scrubbed.
type scrubCore struct{ zapcore.Core }

func (c scrubCore) With(fields []zapcore.Field) zapcore.Core {
	return scrubCore{c.Core.With(scrubFields(fields))}
}

func (c scrubCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}

	return ce
}

func (c scrubCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	ent.Message = scrubText(ent.Message)

	return c.Core.Write(ent, scrubFields(fields)) //nolint:wrapcheck // zapcore contract, returned as is
}

// scrubFields returns fields with the sensitive ones rewritten. The input is
// never modified (zap reuses it for the other cores of a tee); the slice is
// copied only when something changes.
func scrubFields(fields []zapcore.Field) []zapcore.Field {
	var out []zapcore.Field

	for i, f := range fields {
		repl, changed := scrubField(f)
		if !changed {
			if out != nil {
				out = append(out, f)
			}

			continue
		}

		if out == nil {
			out = make([]zapcore.Field, i, len(fields)+1)
			copy(out, fields[:i])
		}

		out = append(out, repl...)
	}

	if out == nil {
		return fields
	}

	return out
}

// scrubField returns the fields that replace f, and whether f changed.
func scrubField(f zapcore.Field) ([]zapcore.Field, bool) {
	switch f.Type {
	case zapcore.ErrorType:
		err, ok := f.Interface.(error)
		if !ok || err == nil {
			return nil, false
		}

		msg := scrubText(err.Error())
		if f.Key != "error" {
			return []zapcore.Field{{Key: f.Key, Type: zapcore.StringType, String: msg}}, true
		}

		// What the OTLP bridge derives from an error field, with the message
		// scrubbed: the bridge keeps attributes the caller set itself.
		return []zapcore.Field{
			{Key: "exception.type", Type: zapcore.StringType, String: fmt.Sprintf("%T", err)},
			{Key: "exception.message", Type: zapcore.StringType, String: msg},
		}, true
	case zapcore.StringType:
		s := scrubValue(f.Key, f.String)

		return []zapcore.Field{{Key: f.Key, Type: zapcore.StringType, String: s}}, s != f.String
	case zapcore.StringerType:
		s, ok := stringerValue(f.Interface)
		if !ok {
			return nil, false
		}

		return []zapcore.Field{{Key: f.Key, Type: zapcore.StringType, String: scrubValue(f.Key, s)}}, true
	default:
		return nil, false
	}
}

// stringerValue calls String on v, as zap does, without letting a panicking
// implementation (a nil pointer receiver) take the caller down; such a value
// is left to zap's own handling.
func stringerValue(v any) (s string, ok bool) {
	st, isStringer := v.(fmt.Stringer)
	if !isStringer {
		return "", false
	}

	defer func() {
		if recover() != nil {
			s, ok = "", false
		}
	}()

	return st.String(), true
}

// scrubValue scrubs the value of the field key.
func scrubValue(key, v string) string {
	switch {
	case queryKeys[key]:
		if v == "" {
			return v
		}

		return redacted
	case urlKeys[key]:
		return scrubURLOrPath(v)
	default:
		return scrubText(v)
	}
}

// scrubURLOrPath scrubs a value that is a URL or a request path.
func scrubURLOrPath(v string) string {
	if strings.Contains(v, "://") {
		return scrubText(v)
	}

	return scrubPath(v)
}

// scrubText scrubs free text: every absolute URL in it is stripped of its
// userinfo, query and fragment and its path scrubbed, tickets standing alone
// are replaced, and the query string of a quoted path is replaced.
func scrubText(s string) string {
	hasURL := strings.Contains(s, "://")
	if !hasURL && len(s) < minTicketLen && !strings.Contains(s, "?") {
		return s
	}

	if hasURL {
		s = urlRe.ReplaceAllStringFunc(s, sanitizeURL)
	}

	if strings.Contains(s, "?") {
		s = pathQueryRe.ReplaceAllString(s, "${1}?"+redacted)
	}

	if len(s) >= minTicketLen {
		s = ticketRe.ReplaceAllString(s, redacted)
	}

	return s
}

// sanitizeURL returns raw as scheme://host/path: no userinfo, no query, no
// fragment, no ticket in the path. A string that does not parse is replaced.
func sanitizeURL(raw string) string {
	// Trailing punctuation of the sentence the URL sits in stays out of it.
	trimmed := strings.TrimRight(raw, ".,;:)]}")
	tail := raw[len(trimmed):]

	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return redacted + tail
	}

	return u.Scheme + "://" + u.Host + scrubPath(u.EscapedPath()) + tail
}

// scrubPath drops the query and fragment of a request path and replaces the
// download tickets in it: the segment after /terraform/dl/ and any segment
// that has the shape of a signed ticket.
func scrubPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}

	if !strings.Contains(p, ".") && !strings.Contains(p, "/dl/") {
		return p
	}

	segs := strings.Split(p, "/")
	for i, seg := range segs {
		afterDL := i >= 2 && segs[i-1] == "dl" && segs[i-2] == "terraform"
		if afterDL || isTicket(seg) {
			segs[i] = redacted
		}
	}

	return strings.Join(segs, "/")
}

func isTicket(seg string) bool {
	return len(seg) >= minTicketLen && ticketRe.FindString(seg) == seg
}
