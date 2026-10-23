// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	gcsEndpoint         = "https://storage.googleapis.com"
	gcsRegion           = "auto"
	gcsSigningService   = "s3"
	gcsGenerationHeader = "X-Goog-Generation"
	gcsComposeTimeout   = 2 * time.Minute
)

// gcsComposer promotes a staged object on Google Cloud Storage with the XML
// API's compose request. A plain CopyObject is one synchronous request whose
// duration grows with the object, so a multi-TiB object can run into request
// time limits; a compose of a single component (the call accepts 1 to 32) only
// stitches object metadata, never rewrites the bytes, takes no time that
// depends on the size, and yields objects up to 5 TiB. The request is signed
// with the HMAC key (AWS Signature V4, as the S3 client does).
type gcsComposer struct {
	base   string
	bucket string
	creds  aws.CredentialsProvider
	signer *v4.Signer
	hc     *http.Client
	now    func() time.Time
}

func newGCSComposer(bucket string, creds aws.CredentialsProvider) *gcsComposer {
	return &gcsComposer{
		base:   gcsEndpoint,
		bucket: bucket,
		creds:  creds,
		signer: v4.NewSigner(),
		hc:     &http.Client{Timeout: gcsComposeTimeout},
		now:    time.Now,
	}
}

type gcsComposeRequest struct {
	XMLName   xml.Name           `xml:"ComposeRequest"`
	Component []gcsComposeObject `xml:"Component"`
}

type gcsComposeObject struct {
	Name              string `xml:"Name"`
	IfGenerationMatch string `xml:"IfGenerationMatch"`
}

// compose creates dst from src exactly as it is at version (its ETag): the
// generation that carries that ETag is read first and pinned in the compose
// request, so an object replaced in between is refused with ErrStagingChanged.
func (c *gcsComposer) compose(ctx context.Context, src, dst, version string, md map[string]string) error {
	generation, err := c.generationOf(ctx, src, version)
	if err != nil {
		return err
	}

	body, err := xml.Marshal(gcsComposeRequest{Component: []gcsComposeObject{{Name: src, IfGenerationMatch: generation}}})
	if err != nil {
		return fmt.Errorf("gcs: encode compose request: %w", err)
	}

	hdr := http.Header{"Content-Type": {"application/octet-stream"}}
	for k, v := range md {
		hdr.Set("x-goog-meta-"+strings.ToLower(k), v)
	}

	resp, err := c.do(ctx, http.MethodPut, dst, "compose", hdr, body)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if err := composeStatus(resp, src, dst); err != nil {
		return err
	}

	return nil
}

// generationOf returns the generation of src after checking its ETag.
func (c *gcsComposer) generationOf(ctx context.Context, src, version string) (string, error) {
	resp, err := c.do(ctx, http.MethodHead, src, "", http.Header{"If-Match": {version}}, nil)
	if err != nil {
		return "", err
	}

	defer func() { _ = resp.Body.Close() }()

	if err := composeStatus(resp, src, src); err != nil {
		return "", err
	}

	generation := resp.Header.Get(gcsGenerationHeader)
	if generation == "" {
		return "", fmt.Errorf("gcs: stat %s: no %s header", src, gcsGenerationHeader) //nolint:err113 // dynamic context
	}

	return generation, nil
}

func composeStatus(resp *http.Response, src, dst string) error {
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrStagingNotFound
	case resp.StatusCode == http.StatusPreconditionFailed:
		return ErrStagingChanged
	}

	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	return fmt.Errorf("gcs: %s %s to %s: HTTP %d: %s", resp.Request.Method, src, dst, resp.StatusCode, strings.TrimSpace(string(msg))) //nolint:err113 // dynamic context
}

// do sends a signed path-style request for key in the bucket.
func (c *gcsComposer) do(ctx context.Context, method, key, rawQuery string, hdr http.Header, body []byte) (*http.Response, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, fmt.Errorf("gcs: endpoint: %w", err)
	}

	u.Path = "/" + c.bucket + "/" + key
	u.RawPath = "/" + url.PathEscape(c.bucket) + "/" + escapeKey(key)
	u.RawQuery = rawQuery

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gcs: build request: %w", err)
	}

	for k, vs := range hdr {
		req.Header[http.CanonicalHeaderKey(k)] = vs
	}

	creds, err := c.creds.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs: credentials: %w", err)
	}

	sum := sha256.Sum256(body)

	if err := c.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), gcsSigningService, gcsRegion, c.now()); err != nil {
		return nil, fmt.Errorf("gcs: sign request: %w", err)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gcs: %s %s: %w", method, key, err)
	}

	return resp, nil
}

// escapeKey percent-encodes each path segment of an object key.
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}

	return strings.Join(parts, "/")
}
