// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

// maxMetadataBytes bounds one member's maven-metadata.xml held in memory.
const maxMetadataBytes = 16 << 20

// mavenMetadataPath splits a maven-metadata.xml request path into the document
// path and its optional checksum suffix.
func mavenMetadataPath(relPath string) (string, string, bool) {
	p := strings.TrimPrefix(relPath, "/")

	for _, s := range []string{"", ".md5", ".sha1", ".sha256", ".sha512"} {
		if doc, found := strings.CutSuffix(p, s); found && path.Base(doc) == "maven-metadata.xml" {
			return doc, s, true
		}
	}

	return "", "", false
}

// MavenHooks is the maven group: merged maven-metadata.xml, everything else first-hit.
var MavenHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: func(
		h *Handler,
		w http.ResponseWriter,
		r *http.Request,
		grp *models.Repository,
		relPath string,
		readable []*models.Repository,
		be Backend,
	) bool {
		base, suffix, ok := mavenMetadataPath(relPath)
		if ok {
			h.serveMavenMetadata(w, r, grp, readable, base, suffix, be)
		}

		return ok
	},
	PackageRef: mavenRef,
}

// serveMavenMetadata answers a maven-metadata.xml (or checksum) request by
// merging the document of every member that has one. With a single
// contributing member its bytes are served unchanged. A member that errors is
// ignored as long as another contributed; with no contributor the group
// answers 404, or 502 if a member failed. Hosted wins: members is already
// narrowed by applyHostedWins, so a metadata document a hosted member holds is
// never merged with the proxies' (unless the group merges proxy versions).
func (h *Handler) serveMavenMetadata(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	doc, suffix string,
	be Backend,
) {
	contentType := "application/xml"
	if suffix != "" {
		contentType = "text/plain"
	}

	// The merge changes with every publish to a member: shared caches
	// revalidate it (maxAge 0) rather than reuse it.
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         memberAsk{path: samePath(doc), limit: maxMetadataBytes},
		what:        "maven metadata",
		contentType: contentType,
		merge: func(c *collection) ([]byte, error) {
			docs := make([][]byte, len(c.docs))
			for i, d := range c.docs {
				docs[i] = d.body
			}

			body, err := mergeDocuments(docs)
			if err != nil || suffix == "" {
				return body, err
			}

			sum, err := checksumOf(body, suffix)
			if err != nil {
				return nil, err
			}

			return []byte(sum + "  " + path.Base(doc) + "\n"), nil
		},
	})
}

// mergeDocuments merges the members' metadata. One document is returned
// verbatim; unparsable documents are an error only when they leave nothing to
// merge.
func mergeDocuments(docs [][]byte) ([]byte, error) {
	if len(docs) == 1 {
		return docs[0], nil
	}

	parsed := make([]*pkgmaven.Metadata, 0, len(docs))

	var firstErr error

	for _, d := range docs {
		m, err := pkgmaven.ParseMetadata(d)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}

			continue
		}

		parsed = append(parsed, m)
	}

	if len(parsed) == 0 {
		return nil, firstErr
	}

	merged, err := pkgmaven.MergeMetadata(parsed...)
	if err != nil {
		return nil, fmt.Errorf("merge: %w", err)
	}

	return merged.Marshal()
}

func checksumOf(body []byte, suffix string) (string, error) {
	md5Hex, sha1Hex, sha256Hex, sha512Hex, err := pkgmaven.ComputeChecksums(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("compute checksums: %w", err)
	}

	switch suffix {
	case ".md5":
		return md5Hex, nil
	case ".sha1":
		return sha1Hex, nil
	case ".sha256":
		return sha256Hex, nil
	default:
		return sha512Hex, nil
	}
}

// captureWriter buffers one member's response in memory, up to limit bytes.
// Created through a mergeSession it also takes the session's gate slot when a
// 2xx response starts (so the gate is never held while the member is still
// waiting on its upstream) and counts its bytes against the request budget. A
// capture that could not get the slot or exceeded a budget is overflow: the
// member counts as failed.
type captureWriter struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	limit    int
	overflow bool

	sess *mergeSession
	err  error // gate wait aborted (request context done)
}

func newCaptureWriter(limit int) *captureWriter {
	return &captureWriter{header: make(http.Header), limit: limit}
}

func (c *captureWriter) Header() http.Header { return c.header }

// code is the status of the capture: a handler that wrote nothing answers an
// empty 200, as net/http does.
func (c *captureWriter) code() int {
	if c.status == 0 {
		return http.StatusOK
	}

	return c.status
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
		c.begin()
	}
}

// begin takes the session's gate slot as a 2xx body starts arriving.
func (c *captureWriter) begin() {
	if c.sess == nil || c.status < http.StatusOK || c.status >= http.StatusMultipleChoices {
		return
	}

	if err := c.sess.acquire(); err != nil {
		c.err = err
		c.overflow = true
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
		c.begin()
	}

	if c.err != nil {
		// Aborts the member's handler: the request is gone.
		return 0, c.err
	}

	if c.body.Len()+len(b) > c.limit || (c.sess != nil && !c.sess.reserve(len(b))) {
		c.overflow = true
		return len(b), nil
	}

	return c.body.Write(b)
}

func mavenRef(relPath string) (PkgRef, bool) {
	if doc, _, ok := mavenMetadataPath(relPath); ok {
		return PkgRef{probe: doc}, true
	}

	rel := strings.TrimPrefix(relPath, "/")

	// A signature or the like of the metadata document belongs to that
	// document's package.
	if strings.HasPrefix(path.Base(rel), "maven-metadata.xml") {
		return PkgRef{probe: path.Dir(rel) + "/maven-metadata.xml", file: true}, true
	}

	// <group path>/<artifact>/<version>/<file>: the artifact's metadata is
	// two levels up from the file.
	if strings.Count(rel, "/") < 3 {
		return PkgRef{}, false
	}

	return PkgRef{probe: path.Dir(path.Dir(rel)) + "/maven-metadata.xml", file: true}, true
}
