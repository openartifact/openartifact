// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

// maxJSONResponse bounds the JSON bodies read from a peer (log pages, units).
const maxJSONResponse = 64 << 20

// HTTPError is a non-success answer of a peer.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("peer answered %d: %s", e.Status, e.Body)
}

// Fatal reports answers retrying cannot fix without operator action
// (credentials refused, repository not federated at the peer).
func (e *HTTPError) Fatal() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

var (
	errHTTPSRequired = fmt.Errorf("%w: peer URL must use https (http is allowed only with %s=true)", ErrInvalid, netutil.EnvAllowPrivate)
	errPeerURL       = fmt.Errorf("%w: peer URL must be an absolute http(s) URL without credentials, query or fragment", ErrInvalid)
)

// ValidatePeerURL checks a peer base URL: absolute http(s), no userinfo, query
// or fragment, https unless private upstreams are explicitly allowed, and an
// address the SSRF guard accepts.
func ValidatePeerURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errPeerURL
	}

	if u.Scheme != "https" && (u.Scheme != "http" || !strings.EqualFold(os.Getenv(netutil.EnvAllowPrivate), "true")) {
		return errHTTPSRequired
	}

	if err := netutil.ValidateOutboundURLContext(ctx, raw); err != nil {
		return fmt.Errorf("%w: peer URL refused: %v", ErrInvalid, err) //nolint:errorlint // client-safe text, the cause is a policy message
	}

	return nil
}

// Client talks to the peer endpoints of one remote member.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// NewClient builds an SSRF-guarded client for a peer. The peer's CA (when set)
// replaces the system roots for this peer only, and its client certificate is
// presented for mutual TLS.
func NewClient(ctx context.Context, p *Peer, secrets PeerSecrets, timeout time.Duration) (*Client, error) {
	if err := ValidatePeerURL(ctx, p.BaseURL); err != nil {
		return nil, err
	}

	base, err := url.Parse(strings.TrimRight(p.BaseURL, "/"))
	if err != nil {
		return nil, errPeerURL
	}

	hc := netutil.SafeHTTPClient(timeout)

	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		return nil, errUnexpectedTransport
	}

	tlsCfg, err := peerTLS(p.CAPEM, secrets.ClientCert, secrets.ClientKey)
	if err != nil {
		return nil, err
	}

	tr.TLSClientConfig = tlsCfg
	// A peer never redirects: following one would carry the token elsewhere.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &Client{base: base, token: secrets.Token, http: hc}, nil
}

// ValidatePeerTLS checks the optional TLS settings of a peer: a CA bundle, and
// a client certificate that must come with its key.
func ValidatePeerTLS(caPEM, clientCert, clientKey string) error {
	_, err := peerTLS(caPEM, clientCert, clientKey)

	return err
}

func peerTLS(caPEM, clientCert, clientKey string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if caPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, fmt.Errorf("%w: peer CA is not valid PEM", ErrInvalid)
		}

		cfg.RootCAs = pool
	}

	if clientCert != "" || clientKey != "" {
		cert, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
		if err != nil {
			return nil, fmt.Errorf("%w: peer client certificate: %v", ErrInvalid, err) //nolint:errorlint // client-safe text
		}

		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	return c.fetch(ctx, path, query, 0)
}

// fetch performs a GET; from > 0 asks for the bytes of the body from that
// offset on, and then a 206 is accepted next to the 200.
func (c *Client) fetch(ctx context.Context, path string, query url.Values, from int64) (*http.Response, error) {
	u := *c.base
	u.Path += "/federation/v1/" + path
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build peer request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	if from > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("peer request %s: %w", path, err)
	}

	if resp.StatusCode == http.StatusOK || (from > 0 && resp.StatusCode == http.StatusPartialContent) {
		return resp, nil
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, strings.TrimSpace(string(body)))
	case http.StatusLocked:
		return nil, ErrQuarantined
	case http.StatusRequestedRangeNotSatisfiable:
		return nil, errRangeUnsatisfiable
	}

	return nil, &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	resp, err := c.get(ctx, path, query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONResponse)).Decode(out); err != nil {
		return fmt.Errorf("decode peer response %s: %w", path, err)
	}

	return nil
}

// Info fetches the peer's log identity and head.
func (c *Client) Info(ctx context.Context) (*InfoResponse, error) {
	var out InfoResponse

	if err := c.getJSON(ctx, "info", nil, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// Log fetches up to limit entries after seq.
func (c *Client) Log(ctx context.Context, after int64, limit int) (*LogResponse, error) {
	var out LogResponse

	q := url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
	if err := c.getJSON(ctx, "log", q, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// Unit fetches the current state of a unit.
func (c *Client) Unit(ctx context.Context, key string) (*UnitResponse, error) {
	var out UnitResponse

	if err := c.getJSON(ctx, "unit", url.Values{"key": {key}}, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// BlobStream is an open blob response.
type BlobStream struct {
	// Body streams the blob from Offset to its end; the caller closes it.
	Body io.ReadCloser
	// Offset is the position in the blob of the first byte of Body: the
	// offset that was asked for when the peer honoured the range, 0 when it
	// answered with the whole blob.
	Offset int64
}

// Blob opens the content of a blob of a unit, from byte offset from on when the
// peer supports ranges (from <= 0 reads it whole). A peer that ignores the
// range answers with the full blob and Offset 0 tells the caller so.
func (c *Client) Blob(ctx context.Context, unitKey, sha256 string, from int64) (*BlobStream, error) {
	resp, err := c.fetch(ctx, "blob", url.Values{"unit": {unitKey}, "sha256": {sha256}}, from)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusPartialContent {
		return &BlobStream{Body: resp.Body}, nil
	}

	cr, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err == nil && cr.start != from {
		err = fmt.Errorf("%w: asked for %d, got %d", errRangeMismatch, from, cr.start)
	}

	if err != nil {
		_ = resp.Body.Close()

		return nil, err
	}

	return &BlobStream{Body: resp.Body, Offset: from}, nil
}
