// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
)

// Go proxy repositories mirror a module proxy (default https://proxy.golang.org)
// speaking the GOPROXY protocol; clients set GOPROXY to the repository URL:
//
//   - "<module>/@v/list" and "<module>/@latest" are cached verbatim with the
//     repository's cache TTL and serve-stale rules.
//   - "<module>/@v/<version>.info", ".mod" and ".zip" are cached under their
//     request path. For a canonical version (v1.2.3, a pseudo-version,
//     +incompatible) they are immutable; ".info" of a query such as a branch
//     name is cached with the TTL. ".mod" and ".zip" of anything but a
//     canonical version are not found.
//
// Module paths and versions travel case-encoded ("!x" is "X") and are cached
// and fetched in that form. ".mod" and ".zip" bodies are verified before they
// are cached (see gosumdb.go); ".info", "@latest" and "list" are checked to be
// what they claim (JSON with the requested Version; one version per line).

// goKind is the kind of GOPROXY resource.
type goKind int

const (
	goList goKind = iota
	goLatest
	goInfo
	goMod
	goZip
)

// maxGoMeta bounds a .info / @latest / list body held in memory to verify it.
const maxGoMeta = 16 << 20

var (
	errGoInfo = errors.New("upstream answer is not a module info document")
	errGoList = errors.New("upstream answer is not a version list")
)

// goRequest is a parsed GOPROXY request path.
type goRequest struct {
	kind goKind
	// module is the unescaped module path; version the unescaped version for
	// .info/.mod/.zip.
	module  string
	version string
	// key is the request path as received (case-encoded), the cache key.
	key string
}

// parseGoPath parses a repository-relative GOPROXY path.
func parseGoPath(key string) (goRequest, bool) {
	if esc, ok := strings.CutSuffix(key, "/@latest"); ok {
		mod, err := module.UnescapePath(esc)
		return goRequest{kind: goLatest, module: mod, key: key}, err == nil
	}

	esc, rest, ok := strings.Cut(key, "/@v/")
	if !ok || rest == "" {
		return goRequest{}, false
	}

	mod, err := module.UnescapePath(esc)
	if err != nil {
		return goRequest{}, false
	}

	if rest == "list" {
		return goRequest{kind: goList, module: mod, key: key}, true
	}

	for _, e := range [...]struct {
		ext  string
		kind goKind
	}{{".info", goInfo}, {".mod", goMod}, {".zip", goZip}} {
		kind := e.kind

		encoded, found := strings.CutSuffix(rest, e.ext)
		if !found || encoded == "" {
			continue
		}

		version, err := module.UnescapeVersion(encoded)
		if err != nil {
			return goRequest{}, false
		}

		// The go command asks for .mod and .zip of canonical versions only.
		if kind != goInfo && !canonicalGoVersion(version) {
			return goRequest{}, false
		}

		return goRequest{kind: kind, module: mod, version: version, key: key}, true
	}

	return goRequest{}, false
}

// canonicalGoVersion reports whether v is a full vMAJOR.MINOR.PATCH[-pre]
// version, optionally +incompatible: a version, not a query.
func canonicalGoVersion(v string) bool {
	if !semver.IsValid(v) {
		return false
	}

	c := semver.Canonical(v)

	return v == c || v == c+"+incompatible"
}

// goImmutable reports whether the file at key is a .info/.mod/.zip of a
// canonical module version, which never changes once published.
func goImmutable(key string) bool {
	req, ok := parseGoPath(key)

	return ok && req.kind != goList && req.kind != goLatest && canonicalGoVersion(req.version)
}

// goUpstreamURL appends the (still case-encoded) request path to the upstream
// base URL, escaped as the go command escapes it ("!" becomes %21).
func goUpstreamURL(baseURL, key string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse upstream url: %w", err)
	}

	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + "/" + strings.ReplaceAll(url.PathEscape(key), "%2F", "/")
	u.Path = strings.TrimRight(u.Path, "/") + "/" + key
	u.RawQuery = ""
	u.Fragment = ""

	return u.String(), nil
}

// serveGo answers a GET/HEAD for a Go proxy repository.
func (s *Service) serveGo(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	key, ok := CleanPath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	req, ok := parseGoPath(key)
	if !ok {
		http.NotFound(w, r)
		return
	}

	s.serveVerbatim(w, r, repo, s.goDoc(req))
}

// goDoc is a GOPROXY resource, cached verbatim under its request path once it
// passes the check for its kind.
func (s *Service) goDoc(req goRequest) metaDoc {
	return metaDoc{
		key: req.key,
		url: func(upstream string) (string, error) { return goUpstreamURL(upstream, req.key) },
		opts: func(context.Context, string) fetchOpts {
			var (
				o       fetchOpts
				vouched bool
			)

			switch req.kind {
			case goList:
				o.verify = verifyGoList
			case goLatest:
				o.verify = verifyGoInfo("")
			case goInfo:
				want := ""
				if canonicalGoVersion(req.version) {
					want = req.version
				}

				o.verify = verifyGoInfo(want)
			case goMod:
				o.vouched, o.verify = &vouched, s.verifyGoMod(req.module, req.version, &vouched)
			case goZip:
				o.vouched, o.verify = &vouched, s.verifyGoZip(req.module, req.version, &vouched)
			}

			return o
		},
		what: "go module file",
	}
}

// verifyGoList refuses a list that is not one version per line: an HTML error
// page served with 200 would otherwise stand in for "no versions" until its
// TTL ends.
func verifyGoList(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	sc := bufio.NewScanner(io.LimitReader(spool, maxGoMeta))
	sc.Buffer(nil, 1<<20)

	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 0 && !semver.IsValid(f[0]) {
			return errGoList
		}
	}

	if sc.Err() != nil {
		return errGoList
	}

	return nil
}

// verifyGoInfo returns the check for a .info / @latest body: a JSON object
// with a Version, which must be want when the request named a canonical one.
func verifyGoInfo(want string) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind spool: %w", err)
		}

		var info struct{ Version string }
		if err := json.NewDecoder(io.LimitReader(spool, maxGoMeta)).Decode(&info); err != nil || info.Version == "" {
			return errGoInfo
		}

		if want != "" && info.Version != want {
			return errGoInfo
		}

		return nil
	}
}

// GoHooks is the proxy hooks of the go format.
var GoHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveGo,
	// .info/.mod/.zip of a canonical version, not list/@latest/queries.
	Immutable:  goImmutable,
	IsMetadata: func(key string) bool { return keyName(key) == "list" || strings.HasSuffix(key, "/@latest") },
	BlobPath:   presign.GoBlobPath,
}
