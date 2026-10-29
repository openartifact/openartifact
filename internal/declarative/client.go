// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes bounds what the engine reads from one API response.
const maxResponseBytes = 64 << 20

// defaultRequestTimeout bounds one API call, from sending the request to
// reading the whole answer. A caller's context with an earlier deadline wins.
// Without it a server that stops answering would hang the run forever: neither
// http.DefaultClient nor the transport of a caller-supplied client sets an
// overall timeout.
const defaultRequestTimeout = 2 * time.Minute

// Client is the minimal authenticated JSON client the engine talks to the
// OpenArtifact REST API with.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// timeout is the longest one call may take.
	timeout time.Duration
}

// NewClient returns a client for the server at baseURL authenticated with an
// API token. httpClient may be nil (http.DefaultClient). Every call is bounded
// by defaultRequestTimeout, whichever http client carries it.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		http:    httpClient,
		timeout: defaultRequestTimeout,
	}
}

// APIError is a non-2xx answer from the server.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// IsStatus reports whether err is an APIError with the given HTTP status.
func IsStatus(err error, status int) bool {
	var ae *APIError

	return errors.As(err, &ae) && ae.Status == status
}

// PathEscape escapes one URL path segment (names may contain unusual runes).
func PathEscape(s string) string { return url.PathEscape(s) }

// Do sends a request with an optional JSON body and decodes a JSON answer into
// out (nil to discard it). Answers outside 2xx become *APIError. The call ends
// with context.DeadlineExceeded after the client's timeout.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var rdr io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s body: %w", method, path, err)
		}

		rdr = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read %s %s: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Message: apiMessage(data)}
	}

	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}

	return nil
}

// Get is Do(GET) decoding into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// apiMessage extracts the human readable message of an API error body.
func apiMessage(body []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Details string `json:"details"`
	}

	_ = json.Unmarshal(body, &e)

	switch {
	case e.Details != "":
		return e.Details
	case e.Message != "":
		return e.Message
	case e.Error != "":
		return e.Error
	}

	return strings.TrimSpace(string(body))
}

// create POSTs body to path and returns the id of the created object. A
// failure reads `create <what> "<name>": ...`.
func (c *Client) create(ctx context.Context, path string, body map[string]any, what, name string) (string, error) {
	var resp struct {
		ID string `json:"id"`
	}

	if err := c.Do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return "", fmt.Errorf("create %s %q: %w", what, name, err)
	}

	return resp.ID, nil
}

// update sends body to path with method and returns the id of live, with or
// without an error. A failure reads `update <what> "<live.Key>": ...`.
func (c *Client) update(ctx context.Context, method, path string, body any, live *Live, what string) (string, error) {
	if err := c.Do(ctx, method, path, body, nil); err != nil {
		return live.ID, fmt.Errorf("update %s %q: %w", what, live.Key, err)
	}

	return live.ID, nil
}

// updateChanges is update with the PatchBody of changes; with no changes it
// sends nothing.
func (c *Client) updateChanges(ctx context.Context, method, path string, live *Live, changes []Change, what string) (string, error) {
	if len(changes) == 0 {
		return live.ID, nil
	}

	return c.update(ctx, method, path, PatchBody(changes), live, what)
}

// remove DELETEs path. A failure reads `delete <what> "<name>": ...`.
func (c *Client) remove(ctx context.Context, path, what, name string) error {
	if err := c.Do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("delete %s %q: %w", what, name, err)
	}

	return nil
}
