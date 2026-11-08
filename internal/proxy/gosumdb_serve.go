// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/module"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Go proxy repositories also relay the checksum database, the way the go
// command expects of a GOPROXY ("GOPROXY/sumdb/<name>/<endpoint>", the sumdb
// proxy protocol): a client that sets GOPROXY to the repository then reaches the
// database through it and needs no egress of its own, which is what a hermetic
// build needs (Hermeto points the go command at the repository and nothing
// else). The endpoints are supported, latest, lookup/<module>@<version> and
// tile/<H>/<L>/<K>[.p/<W>].
//
// This is a relay, not the verification of gosumdb.go: the go command checks
// the signed tree head, the inclusion proof and the hashes itself, so the
// answers are cached as the upstream sent them.
//
//   - Only the databases in the allow-list (Service.WithGoSumdbHosts, default
//     sum.golang.org) are reachable, always over https at the database's own
//     host: the request cannot name another upstream, and the repository's
//     upstream URL and credentials play no part. A name outside the list is 404,
//     which tells the go command the proxy does not serve that database, so it
//     asks the database directly (as it does when GOPROXY is "direct").
//   - "supported" is answered here, without a request upstream.
//   - A tile is addressed by its position and never changes: it is cached as
//     immutable and served with the upstream down. "latest" and "lookup" carry
//     the current signed tree head and are cached for the repository's TTL,
//     served stale when the upstream fails like any metadata of a proxy.
//   - The body of a lookup, latest or tile that cannot be what it claims (an
//     HTML error page served with 200, a hash tile of the wrong length) is
//     refused with 502 and not stored.
//   - A hosted Go repository holds no database: it answers 404 (format/gomod).

// sumdbDir is the key prefix of the checksum database relay.
const sumdbDir = "sumdb/"

// sumdbHashLen is the length in bytes of one tile hash.
const sumdbHashLen = 32

// sumdbNoteSep starts the signature lines of a signed note.
const sumdbNoteSep = "\n\n\u2014 "

// sumdbKind is the kind of checksum database endpoint.
type sumdbKind int

const (
	sumdbSupported sumdbKind = iota
	sumdbLatest
	sumdbLookup
	sumdbTile
)

var (
	errSumdbNote = errors.New("answer is not a signed note")
	errSumdbTile = errors.New("answer is not the tile requested")

	// sumdbTileRE matches tile/<H>/<L>/<K>[.p/<W>]: H the tile height, L the
	// level (or "data"), K the index in three-digit groups ("x001/234"), W the
	// width of a partial tile.
	sumdbTileRE = regexp.MustCompile(`^tile/([0-9]{1,2})/(data|[0-9]{1,2})/(?:x[0-9]{3}/)*[0-9]{3}(?:\.p/([0-9]{1,3}))?$`)
)

// sumdbRequest is a parsed relay request path.
type sumdbRequest struct {
	kind sumdbKind
	// host is the database name, path the endpoint below it and key the whole
	// request path, which is the cache key.
	host, path, key string
	// width is the number of hashes a hash tile holds: the full 1<<height unless
	// the request is for a partial tile. data marks a data tile, whose records
	// have no fixed length.
	width int
	data  bool
}

// parseSumdbPath parses a repository-relative "sumdb/<name>/<endpoint>" path.
func parseSumdbPath(key string) (sumdbRequest, bool) {
	rest, ok := strings.CutPrefix(key, sumdbDir)
	if !ok {
		return sumdbRequest{}, false
	}

	host, endpoint, ok := strings.Cut(rest, "/")
	if !ok || host == "" {
		return sumdbRequest{}, false
	}

	req := sumdbRequest{host: host, path: endpoint, key: key}

	switch {
	case endpoint == "supported":
		req.kind = sumdbSupported
	case endpoint == "latest":
		req.kind = sumdbLatest
	case strings.HasPrefix(endpoint, "lookup/"):
		req.kind = sumdbLookup

		return req, validSumdbLookup(strings.TrimPrefix(endpoint, "lookup/"))
	case strings.HasPrefix(endpoint, "tile/"):
		return parseSumdbTile(req)
	default:
		return sumdbRequest{}, false
	}

	return req, true
}

// validSumdbLookup reports whether m is "<escaped module>@<escaped version>" of
// a canonical version: the only thing the database has a record of.
func validSumdbLookup(m string) bool {
	path, ver, found := strings.CutLast(m, "@")
	if !found {
		return false
	}

	if _, err := module.UnescapePath(path); err != nil {
		return false
	}

	version, err := module.UnescapeVersion(ver)

	return err == nil && canonicalGoVersion(version)
}

// parseSumdbTile fills in the tile fields of req, whose path is "tile/...".
func parseSumdbTile(req sumdbRequest) (sumdbRequest, bool) {
	m := sumdbTileRE.FindStringSubmatch(req.path)
	if m == nil {
		return sumdbRequest{}, false
	}

	height, _ := strconv.Atoi(m[1]) // two digits by the pattern

	// The go command uses height 8; a height beyond what 1<<height hashes of a
	// reasonable size allow is no tile.
	if height < 1 || height > 10 {
		return sumdbRequest{}, false
	}

	req.kind, req.data, req.width = sumdbTile, m[2] == "data", 1<<height

	if m[3] != "" {
		width, _ := strconv.Atoi(m[3]) // three digits by the pattern
		if width < 1 || width >= req.width {
			return sumdbRequest{}, false
		}

		req.width = width
	}

	return req, true
}

// goSumdbImmutable reports whether the file at key is a tile of the checksum
// database, whose content its position determines for good.
func goSumdbImmutable(key string) bool {
	req, ok := parseSumdbPath(key)

	return ok && req.kind == sumdbTile
}

// WithGoSumdbHosts sets the checksum databases Go proxy repositories relay
// ("sum.golang.org" by default): each is reached over https at its own host.
// Empty entries are ignored; an empty list turns the relay off. It must be
// called before the service serves requests.
func (s *Service) WithGoSumdbHosts(hosts []string) *Service {
	s.sumdbUpstreams = make(map[string]string, len(hosts))

	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			s.sumdbUpstreams[h] = "https://" + h
		}
	}

	return s
}

// serveGoSumdb answers a GET/HEAD of the checksum database relay.
func (s *Service) serveGoSumdb(w http.ResponseWriter, r *http.Request, repo *models.Repository, key string) {
	req, ok := parseSumdbPath(key)
	if !ok {
		http.NotFound(w, r)
		return
	}

	upstream, ok := s.sumdbUpstreams[req.host]
	if !ok {
		http.NotFound(w, r)
		return
	}

	if req.kind == sumdbSupported {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)

		return
	}

	s.serveVerbatim(w, r, repo, metaDoc{
		key: req.key,
		url: func(string) (string, error) { return goUpstreamURL(upstream, req.path) },
		opts: staticOpts(fetchOpts{
			// The repository's credentials belong to its own upstream.
			noAuth: true,
			verify: verifySumdb(req),
		}),
		limit: maxSumdbBody,
		what:  "go checksum database file",
	})
}

// verifySumdb returns the check for the body of req: a signed note for latest
// and lookup, as many hashes as the tile has for a hash tile. The go command
// verifies the content itself; this only keeps an answer that is not the file
// (a captive portal's page) out of the cache.
func verifySumdb(req sumdbRequest) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind spool: %w", err)
		}

		body, err := io.ReadAll(io.LimitReader(spool, maxSumdbBody))
		if err != nil {
			return fmt.Errorf("read spool: %w", err)
		}

		switch {
		case req.kind != sumdbTile:
			if !bytes.Contains(body, []byte(sumdbNoteSep)) {
				return errSumdbNote
			}
		case req.data:
			if len(body) == 0 {
				return errSumdbTile
			}
		case len(body) != req.width*sumdbHashLen:
			return errSumdbTile
		}

		return nil
	}
}
