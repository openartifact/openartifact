// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"mime"
	"strconv"
	"strings"
)

// Content types of the simple API responses.
const (
	ContentTypeJSON    = "application/vnd.pypi.simple.v1+json"
	ContentTypeHTMLv1  = "application/vnd.pypi.simple.v1+html"
	ContentTypeHTMLRaw = "text/html; charset=utf-8"
)

// UpstreamAccept is the Accept header sent to upstream indexes: PEP 691 JSON
// when the upstream has it, HTML otherwise.
const UpstreamAccept = ContentTypeJSON + ", " + ContentTypeHTMLv1 + ";q=0.2, text/html;q=0.1"

// Offer is the serialization chosen for a response.
type Offer struct {
	Format      Format
	ContentType string
}

// Negotiate picks the response serialization for an Accept header (PEP 691).
// An empty header or one that does not prefer JSON yields HTML, the legacy
// default. ok is false when the header accepts neither serialization.
func Negotiate(accept string) (Offer, bool) {
	if strings.TrimSpace(accept) == "" {
		return Offer{HTML, ContentTypeHTMLRaw}, true
	}

	var htmlM, jsonM match

	for part := range strings.SplitSeq(accept, ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}

		q := 1.0

		if qs, has := params["q"]; has {
			if v, perr := strconv.ParseFloat(qs, 64); perr == nil {
				q = v
			}
		}

		switch mt {
		case ContentTypeJSON, "application/vnd.pypi.simple.latest+json":
			jsonM.add(q, 3, ContentTypeJSON)
		case ContentTypeHTMLv1, "application/vnd.pypi.simple.latest+html":
			htmlM.add(q, 3, ContentTypeHTMLv1)
		case "text/html":
			htmlM.add(q, 3, ContentTypeHTMLRaw)
		case "application/*":
			jsonM.add(q, 1, ContentTypeJSON)
		case "text/*":
			htmlM.add(q, 1, ContentTypeHTMLRaw)
		case "*/*":
			jsonM.add(q, 0, ContentTypeJSON)
			htmlM.add(q, 0, ContentTypeHTMLRaw)
		}
	}

	switch {
	case jsonM.q > 0 && jsonM.q > htmlM.q:
		return Offer{JSON, jsonM.contentType}, true
	case htmlM.q > 0:
		return Offer{HTML, htmlM.contentType}, true
	}

	return Offer{}, false
}

// match is the best-specified Accept range seen for one serialization (RFC
// 9110 section 12.5.1: the most specific matching range sets the quality).
type match struct {
	q           float64
	specificity int
	contentType string
	seen        bool
}

func (m *match) add(q float64, specificity int, contentType string) {
	if m.seen && specificity < m.specificity {
		return
	}

	if m.seen && specificity == m.specificity && q <= m.q {
		return
	}

	m.seen, m.q, m.specificity, m.contentType = true, q, specificity, contentType
}
