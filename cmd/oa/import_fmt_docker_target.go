// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The destination side of a docker import speaks the OCI distribution API that
// OpenArtifact serves under /v2/: blobs are pushed through monolithic uploads
// (or mounted from another image of the same repository), then manifests are
// put by digest and finally by tag. The importer's admin token authenticates
// the requests exactly as it does for the other formats.

const (
	ociDigestHeader  = "Docker-Content-Digest"
	ociSubjectHeader = "Oci-Subject"
	ociCancelTimeout = 30 * time.Second
)

var errOCIUploadLocation = errors.New("the registry did not say where to send the blob")

// ociRepo is one image of a hosted docker repository on the destination.
type ociRepo struct {
	t *importTarget
	// prefix is the API path of the image: /v2/<repository>/<image>.
	prefix string
	// name is how the destination's package listing names the image's tags.
	name string
}

func newOCIRepo(t *importTarget, dst, image string) ociRepo {
	return ociRepo{t: t, prefix: "/v2/" + url.PathEscape(dst) + "/" + escapePath(image), name: dst + "/" + image}
}

// hasBlob reports whether the image can already see the blob.
func (o ociRepo) hasBlob(ctx context.Context, digest string) (bool, error) {
	resp, err := o.t.request(ctx, http.MethodHead, o.prefix+"/blobs/"+digest, nil, -1, nil)
	if err != nil {
		return false, err
	}

	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, serverError(resp, "HEAD "+o.prefix+"/blobs/"+digest)
	}
}

// pushBlob stores one blob and returns how many bytes travelled. With from set
// (an image of the same repository known to hold the blob) the registry is
// asked to link it instead; it falls back to an upload when it cannot. The
// bytes read from open are hashed on the way, so a blob that does not match its
// digest is reported as the source's fault rather than the registry's.
func (o ociRepo) pushBlob(ctx context.Context, d ociDescriptor, from string, open func(context.Context) (io.ReadCloser, error)) (int64, error) {
	session, mounted, err := o.startUpload(ctx, d.Digest, from)
	if err != nil || mounted {
		return 0, err
	}

	body, err := open(ctx)
	if err != nil {
		o.cancelUpload(ctx, session)

		return 0, fmt.Errorf("read the blob from the source: %w", err)
	}

	defer func() { _ = body.Close() }()

	hr := newHashingReader(body)
	err = o.finishUpload(ctx, session, d, hr)
	sums := hr.sums()

	if sums.n == d.Size && ociDigestPrefix+sums.sha256 != d.Digest {
		o.cancelUpload(ctx, session)

		return 0, fmt.Errorf("%w: blob %s: the source sent content of %s%s (%d bytes)",
			errImportVerify, d.Digest, ociDigestPrefix, sums.sha256, sums.n)
	}

	if err != nil {
		o.cancelUpload(ctx, session)

		return 0, err
	}

	return sums.n, nil
}

// startUpload opens an upload session, or mounts the blob when from names an
// image that holds it. It returns the session path, or mounted when the blob
// is in place without any bytes being sent.
func (o ociRepo) startUpload(ctx context.Context, digest, from string) (string, bool, error) {
	apiPath := o.prefix + "/blobs/uploads/"
	if from != "" {
		apiPath += "?mount=" + url.QueryEscape(digest) + "&from=" + url.QueryEscape(from)
	}

	resp, err := o.t.send(ctx, http.MethodPost, apiPath, nil, 0, nil)
	if err != nil {
		return "", false, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusCreated {
		return "", true, nil
	}

	session, err := ociSessionPath(resp.Header.Get("Location"))

	return session, false, err
}

// ociSessionPath turns the Location of an upload session into a path on the
// importer's own server. Only the path and query are used: the token is never
// sent to whatever host a reverse proxy may have put into the URL.
func ociSessionPath(location string) (string, error) {
	u, err := url.Parse(location)
	if err != nil || u.Path == "" {
		return "", fmt.Errorf("%w: %q", errOCIUploadLocation, location)
	}

	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}

	return p, nil
}

// finishUpload sends the whole blob in the closing request of the session.
func (o ociRepo) finishUpload(ctx context.Context, session string, d ociDescriptor, body io.Reader) error {
	sep := "?"
	if strings.Contains(session, "?") {
		sep = "&"
	}

	hdr := http.Header{"Content-Type": {"application/octet-stream"}}

	resp, err := o.t.send(ctx, http.MethodPut, session+sep+"digest="+url.QueryEscape(d.Digest), body, d.Size, hdr)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get(ociDigestHeader); got != "" && got != d.Digest {
		return fmt.Errorf("%w: blob %s was stored as %s", errImportVerify, d.Digest, got)
	}

	return nil
}

// cancelUpload drops a session that will not be completed, so it does not
// linger until the server's timeout. It is best effort and runs even when the
// import was cancelled.
func (o ociRepo) cancelUpload(ctx context.Context, session string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ociCancelTimeout)
	defer cancel()

	resp, err := o.t.request(ctx, http.MethodDelete, session, nil, -1, nil)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// putManifest stores m under ref (a tag or its own digest) and returns the
// digest the registry reports for it.
func (o ociRepo) putManifest(ctx context.Context, ref string, m ociManifest, meta uploadMeta) (string, error) {
	hdr := meta.header()
	hdr.Set("Content-Type", m.MediaType)

	resp, err := o.t.send(ctx, http.MethodPut, o.prefix+"/manifests/"+url.PathEscape(ref), bytes.NewReader(m.Body), int64(len(m.Body)), hdr)
	if err != nil {
		return "", err
	}

	defer func() { _ = resp.Body.Close() }()

	got := resp.Header.Get(ociDigestHeader)
	if got == "" {
		return "", fmt.Errorf("%w: the registry did not report the digest of manifest %s", errImportVerify, ref)
	}

	// A registry that processed the subject of a referrer says so; one that
	// did not stored an artifact its referrers listing will never show.
	if s := m.doc.Subject; s != nil && resp.Header.Get(ociSubjectHeader) != s.Digest {
		return "", fmt.Errorf("%w: the registry stored manifest %s without linking it to its subject %s", errImportVerify, ref, s.Digest)
	}

	return got, nil
}

// hasManifest reports whether the image holds the manifest with the digest,
// tagged or not.
func (o ociRepo) hasManifest(ctx context.Context, digest string) (bool, error) {
	resp, err := o.t.request(ctx, http.MethodHead, o.prefix+"/manifests/"+url.PathEscape(digest), nil, -1, nil)
	if err != nil {
		return false, err
	}

	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, serverError(resp, "HEAD "+o.prefix+"/manifests/"+digest)
	}
}

// removeManifest deletes the manifest with the given digest, and with it the
// tags that point at it.
func (o ociRepo) removeManifest(ctx context.Context, digest string) error {
	return o.t.deletePath(ctx, o.prefix+"/manifests/"+url.PathEscape(digest))
}
