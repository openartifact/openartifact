// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/md5"  //nolint:gosec // verifies the md5 sidecar an upstream publishes; corruption check, not authentication
	"crypto/sha1" //nolint:gosec // verifies the sha1 sidecar an upstream publishes; corruption check, not authentication
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
)

// Ivy and sbt proxies mirror an upstream by path, like Maven and raw: the
// layout (Ivy `[org]/[module]/[revision]/[type]s/[artifact]-[revision].[ext]`,
// the sbt plugin layout with its scala_/sbt_ directories, or the Maven layout
// of `m2compatible` resolvers) is the upstream's business, only the path is
// validated. What they add over plain mirroring is verification: a file is
// checked against the checksum sidecar the upstream publishes next to it
// (<file>.sha256, else .sha1, else .md5), and only a verified release is
// cached as immutable.

const (
	// maxIvyDigestBytes bounds a checksum sidecar read for verification; the
	// real ones are a hex digest and at most a file name.
	maxIvyDigestBytes = 4096

	// ivySidecarTimeout bounds one checksum sidecar request, on top of the
	// fetch's own deadline.
	ivySidecarTimeout = 30 * time.Second
)

// ivyDigest is one kind of checksum sidecar.
type ivyDigest struct {
	ext  string
	size int
	new  func() hash.Hash
}

// ivyDigests lists the sidecars verification tries, strongest first.
var ivyDigests = []ivyDigest{ //nolint:gochecknoglobals // read-only table
	{ext: ".sha256", size: sha256.Size, new: sha256.New},
	{ext: ".sha1", size: sha1.Size, new: sha1.New},
	{ext: ".md5", size: md5.Size, new: md5.New},
}

// ivySidecarExts are the checksum and signature files published next to a
// file; they are served as the upstream has them and never verified.
var ivySidecarExts = []string{".md5", ".sha1", ".sha256", ".sha512", ".asc"} //nolint:gochecknoglobals // read-only table

// ivyPackageExts are the file names an upstream never legitimately answers
// with an HTML page (see isHTMLForPackage).
var ivyPackageExts = []string{ //nolint:gochecknoglobals // read-only table
	".jar", ".pom", ".xml", ".zip", ".war", ".ear", ".aar", ".module", ".gz", ".tgz",
	".md5", ".sha1", ".sha256", ".sha512", ".asc",
}

// isIvySidecar reports whether key is a checksum or signature file.
func isIvySidecar(key string) bool {
	for _, ext := range ivySidecarExts {
		if strings.HasSuffix(key, ext) {
			return true
		}
	}

	return false
}

// isIvyMetadata reports whether key is a document the repository regenerates in
// place (maven-metadata*.xml of an `m2compatible` layout, snapshot listings):
// it is never verified or pinned, and is capped like other metadata.
func isIvyMetadata(key string) bool {
	return strings.HasPrefix(key[strings.LastIndexByte(key, '/')+1:], "maven-metadata")
}

// ivyImmutable reports whether key is a released file whose name is never
// reused: not a checksum or signature (served for the repository TTL, so one
// republished upstream is picked up), metadata or snapshot file. The check is
// only half of the pin: a file is cached as immutable once its body matched
// the upstream's checksum (see ivyFetchOpts).
func ivyImmutable(key string) bool {
	return !isIvySidecar(key) && !isIvyMetadata(key) && !strings.Contains(key, "-SNAPSHOT")
}

// ivyFetchOpts returns the options for fetching key from an ivy or sbt
// upstream. A verifiable file is checked against its checksum sidecar once its
// body is spooled; a mismatch refuses the body, and a file whose upstream
// publishes no checksum is accepted but cached for the repository TTL only.
func (s *Service) ivyFetchOpts(ctx context.Context, repo *models.Repository, key string) fetchOpts {
	if isIvySidecar(key) || isIvyMetadata(key) {
		return fetchOpts{}
	}

	vouched := false

	return fetchOpts{
		vouched: &vouched,
		verify: func(spool io.ReadSeeker) error {
			ok, err := s.verifyIvyFile(ctx, repo, key, spool)
			vouched = ok

			return err
		},
	}
}

// verifyIvyFile compares the spooled body with the first checksum sidecar the
// upstream has for key. ok is false when it has none (the file is then
// unverified); a failing sidecar request is an error, a missing one is not.
func (s *Service) verifyIvyFile(ctx context.Context, repo *models.Repository, key string, spool io.ReadSeeker) (bool, error) {
	for _, d := range ivyDigests {
		want, found, err := s.fetchIvyDigest(ctx, repo, key, d)
		if err != nil {
			return false, err
		}

		if !found {
			continue
		}

		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return false, fmt.Errorf("rewind spool: %w", err)
		}

		h := d.new()
		if _, err := io.Copy(h, spool); err != nil {
			return false, fmt.Errorf("hash spool: %w", err)
		}

		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return false, fmt.Errorf("%w: %s is %s, upstream %s says %s", errIvyChecksum, key, got, d.ext, want)
		}

		return true, nil
	}

	return false, nil
}

var errIvyChecksum = errors.New("file does not match its published checksum")

// fetchIvyDigest reads the d sidecar of key from the upstream. found is false
// when the upstream has none or it holds no usable digest (the file is then
// not verified by it).
func (s *Service) fetchIvyDigest(ctx context.Context, repo *models.Repository, key string, d ivyDigest) (string, bool, error) {
	target, err := upstreamURL(repo.UpstreamURL, key+d.ext)
	if err != nil {
		return "", false, err
	}

	ctx, cancel := context.WithTimeout(ctx, ivySidecarTimeout)
	defer cancel()

	if verr := netutil.ValidateOutboundURLContext(ctx, target); verr != nil {
		return "", false, fmt.Errorf("checksum url: %w", verr)
	}

	req, err := s.newUpstreamRequest(ctx, repo, target, fetchOpts{})
	if err != nil {
		return "", false, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("fetch %s checksum: %w", d.ext, err)
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return "", false, nil
	case resp.StatusCode != http.StatusOK:
		return "", false, fmt.Errorf("%w: %s checksum answered %d", errUpstream, d.ext, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIvyDigestBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("read %s checksum: %w", d.ext, err)
	}

	want, ok := parseIvyDigest(body, d.size)

	return want, ok, nil
}

// parseIvyDigest extracts the lowercase hex digest of size bytes from a
// checksum file: its first whitespace-separated field (`<hex>`, or
// `<hex>  <file name>` as sha1sum prints it). Anything else, including a body
// larger than a checksum file can be, holds no digest.
func parseIvyDigest(body []byte, size int) (string, bool) {
	if len(body) > maxIvyDigestBytes {
		return "", false
	}

	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", false
	}

	raw, err := hex.DecodeString(fields[0])
	if err != nil || len(raw) != size {
		return "", false
	}

	return hex.EncodeToString(raw), true
}

// IvyHooks is the proxy hooks of the ivy format; the sbt format shares them
// (SbtHooks): the two layouts differ in the upstream's directory naming only.
var IvyHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	// Released files that matched the upstream's checksum; see ivyFetchOpts.
	Fetch:       (*Service).fetchIvy,
	Immutable:   ivyImmutable,
	IsMetadata:  func(key string) bool { return isIvyMetadata(key) || isIvySidecar(key) || keyName(key) == "ivy.xml" },
	PackageExts: ivyPackageExts,
}

// SbtHooks is the proxy hooks of the sbt format: the same as IvyHooks.
var SbtHooks = IvyHooks //nolint:gochecknoglobals // format hook table

// fetchIvy is Hooks.Fetch of ivy and sbt: a verifiable file is checked against
// the checksum sidecar the upstream publishes.
func (s *Service) fetchIvy(ctx context.Context, repo *models.Repository, key, target string) (*models.ProxyCacheEntry, error) {
	return s.fetchURL(ctx, repo, key, target, s.ivyFetchOpts(ctx, repo, key))
}
