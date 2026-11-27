// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// ErrBadURL is returned for an upstream or document URL that is not an
// absolute http(s) URL.
var ErrBadURL = errors.New("p2: not an absolute http(s) URL")

// Relocator maps the locations of a composite repository's children onto keys
// of the proxy. The proxy serves the files below its upstream URL under the same
// relative path, and the rest of the upstream host under HostPrefix, so a
// child that the upstream names by absolute URL, or by a relative path that
// leaves the repository, is reached through the proxy too.
type Relocator struct {
	base *url.URL
	// dir is the URL of the directory of the document, which relative locations
	// resolve against.
	dir *url.URL
	// from is the directory of the document's key, as path segments.
	from []string
}

// NewRelocator returns the relocator of the document stored under key, which is
// fetched from docURL, of a proxy whose upstream is upstream.
func NewRelocator(upstream, docURL, key string) (*Relocator, error) {
	base, err := parseHTTP(upstream)
	if err != nil {
		return nil, err
	}

	doc, err := parseHTTP(docURL)
	if err != nil {
		return nil, err
	}

	doc.RawQuery, doc.Fragment = "", ""
	doc.Path = path.Dir(doc.Path)

	if !strings.HasSuffix(doc.Path, "/") {
		doc.Path += "/"
	}

	doc.RawPath = ""

	var from []string
	if dir := path.Dir(key); dir != "." {
		from = strings.Split(dir, "/")
	}

	return &Relocator{base: base, dir: doc, from: from}, nil
}

func parseHTTP(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("%w: %q", ErrBadURL, raw)
	}

	return u, nil
}

// Relocate returns the location a client must use for the child at location,
// and reports whether it differs from location. A child on another host, and
// any location that carries a query or fragment (a download redirector, not a
// repository directory), are left as they are: the client reaches them
// directly.
func (r *Relocator) Relocate(location string) (string, bool) {
	ref, err := url.Parse(strings.TrimSpace(location))
	if err != nil || ref.RawQuery != "" || ref.Fragment != "" || ref.Opaque != "" || ref.User != nil {
		return location, false
	}

	abs := r.dir.ResolveReference(ref)
	if (abs.Scheme != "http" && abs.Scheme != "https") || !sameHost(abs, r.base) {
		return location, false
	}

	key := r.keyOf(abs.Path)
	if key == "" {
		key = "."
	}

	// An empty segment ("a//b") has no proxy key: the proxy refuses such paths.
	if strings.Contains(key, "//") || strings.HasPrefix(key, "/") {
		return location, false
	}

	out := r.relative(key)
	if strings.HasSuffix(abs.Path, "/") && !strings.HasSuffix(out, "/") {
		out += "/"
	}

	return out, out != location
}

// sameHost reports whether a and b name the same host and port; a plain-http and
// a https URL of one host count as the same (the proxy fetches with the scheme of
// its upstream).
func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Hostname(), b.Hostname()) && a.Port() == b.Port()
}

// keyOf is the proxy key of an absolute path of the upstream host: the path
// below the upstream URL, or the path under HostPrefix when it lies outside.
func (r *Relocator) keyOf(p string) string {
	basePath := strings.TrimRight(r.base.Path, "/")

	switch {
	case p == basePath || p == basePath+"/":
		return ""
	case strings.HasPrefix(p, basePath+"/"):
		return strings.Trim(p[len(basePath)+1:], "/")
	default:
		return HostPrefix + "/" + strings.Trim(p, "/")
	}
}

// relative is the path from the directory of the document to key.
func (r *Relocator) relative(key string) string {
	if key == "." {
		return cmp.Or(strings.Repeat("../", len(r.from)), "./")
	}

	to := strings.Split(key, "/")

	common := 0
	for common < len(r.from) && common < len(to)-1 && r.from[common] == to[common] {
		common++
	}

	parts := make([]string, 0, len(r.from)-common+len(to)-common)
	for range len(r.from) - common {
		parts = append(parts, "..")
	}

	for _, seg := range to[common:] {
		parts = append(parts, url.PathEscape(seg))
	}

	return strings.Join(parts, "/")
}

// UpstreamURL maps a key of the proxy to the URL it is fetched from: the key
// below the upstream URL, or for a HostPrefix key the path on the upstream host.
// ok is false when key addresses the host namespace with nothing below it.
func UpstreamURL(upstream, key string) (string, bool) {
	base, err := parseHTTP(upstream)
	if err != nil {
		return "", false
	}

	u := *base
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""

	if rest, ok := strings.CutPrefix(key, HostPrefix+"/"); ok {
		if rest == "" {
			return "", false
		}

		u.Path = "/" + rest
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + key
	}

	return u.String(), true
}
