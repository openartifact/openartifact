// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dtrack pushes OpenArtifact's SBOMs and VEX decisions to a
// Dependency-Track server (https://dependencytrack.org) and records how far
// each push got. See docs/security/dependency-track.md.
package dtrack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
)

const (
	apiKeyHeader   = "X-Api-Key" //nolint:gosec // header name, not a credential
	requestTimeout = 60 * time.Second
	// maxResponseBytes bounds any Dependency-Track response read into memory
	// (a project with many findings is a few MiB at most).
	maxResponseBytes = 64 << 20
)

// Permissions the integration uses; Check reports which ones the API key lacks.
const (
	PermBOMUpload       = "BOM_UPLOAD"
	PermProjectCreation = "PROJECT_CREATION_UPLOAD"
	PermViewPortfolio   = "VIEW_PORTFOLIO"
	PermViewVuln        = "VIEW_VULNERABILITY"
	PermVulnAnalysis    = "VULNERABILITY_ANALYSIS"
)

// ErrNotFound is returned when Dependency-Track answers 404.
var ErrNotFound = errors.New("dependency-track: not found")

// ErrInvalidURL is returned for a base URL that is not an absolute http(s) URL.
var ErrInvalidURL = errors.New("dependency-track URL must be an absolute http(s) URL")

// HTTPError is a non-2xx answer from Dependency-Track. The body is not kept: it
// can echo request data and is never shown to API clients.
type HTTPError struct {
	Status int
	Op     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("dependency-track %s: HTTP %d", e.Op, e.Status)
}

// Client talks to the REST API of one Dependency-Track server.
type Client struct {
	base *url.URL
	key  string
	hc   *http.Client
}

// NewClient validates baseURL and returns a client that refuses to connect to
// internal addresses (netutil's SSRF guard, enforced at dial time).
func NewClient(baseURL, apiKey string) (*Client, error) {
	return newClient(baseURL, apiKey, telemetry.WrapClient(netutil.SafeHTTPClient(requestTimeout)))
}

// NewClientWithHTTP is NewClient with a caller-supplied HTTP client (tests).
func NewClientWithHTTP(baseURL, apiKey string, hc *http.Client) (*Client, error) {
	return newClient(baseURL, apiKey, hc)
}

func newClient(baseURL, apiKey string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInvalidURL
	}

	return &Client{base: u, key: apiKey, hc: hc}, nil
}

func (c *Client) endpoint(path string, q url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()

	return u.String()
}

func (c *Client) do(ctx context.Context, op, method, target string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("dependency-track %s: %w", op, err)
	}

	req.Header.Set(apiKeyHeader, c.key)
	req.Header.Set("Accept", "application/json")

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dependency-track %s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("dependency-track %s: read response: %w", op, err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, op)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, &HTTPError{Status: resp.StatusCode, Op: op}
	}

	return data, nil
}

func (c *Client) getJSON(ctx context.Context, op, path string, q url.Values, out any) error {
	data, err := c.do(ctx, op, http.MethodGet, c.endpoint(path, q), nil, "")
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("dependency-track %s: decode response: %w", op, err)
	}

	return nil
}

// multipartBody builds a multipart/form-data body of text fields and one file part.
func multipartBody(fields map[string]string, fileField, fileName string, file []byte) (io.Reader, string, error) {
	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)

	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, "", fmt.Errorf("multipart field: %w", err)
		}
	}

	fw, err := mw.CreateFormFile(fileField, fileName)
	if err != nil {
		return nil, "", fmt.Errorf("multipart file: %w", err)
	}

	if _, err := fw.Write(file); err != nil {
		return nil, "", fmt.Errorf("multipart file: %w", err)
	}

	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("multipart close: %w", err)
	}

	return &buf, mw.FormDataContentType(), nil
}

func (c *Client) uploadToken(ctx context.Context, op, path string, fields map[string]string, fileField, fileName string, file []byte) (string, error) {
	body, ct, err := multipartBody(fields, fileField, fileName, file)
	if err != nil {
		return "", err
	}

	data, err := c.do(ctx, op, http.MethodPost, c.endpoint(path, nil), body, ct)
	if err != nil {
		return "", err
	}

	var out struct {
		Token string `json:"token"`
	}

	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("dependency-track %s: no processing token in response", op) //nolint:err113 // upstream protocol violation
	}

	return out.Token, nil
}

// UploadBOM uploads a CycloneDX BOM, creating the project when it does not exist.
// It returns the processing token.
func (c *Client) UploadBOM(ctx context.Context, projectName, projectVersion string, bom []byte) (string, error) {
	fields := map[string]string{"projectName": projectName, "autoCreate": "true"}
	if projectVersion != "" {
		fields["projectVersion"] = projectVersion
	}

	return c.uploadToken(ctx, "upload BOM", "/api/v1/bom", fields, "bom", "bom.json", bom)
}

// UploadVEX uploads a CycloneDX VEX document to an existing project.
func (c *Client) UploadVEX(ctx context.Context, projectUUID string, vex []byte) (string, error) {
	return c.uploadToken(ctx, "upload VEX", "/api/v1/vex", map[string]string{"project": projectUUID}, "vex", "vex.json", vex)
}

// Processing reports whether Dependency-Track still processes the upload behind token.
func (c *Client) Processing(ctx context.Context, token string) (bool, error) {
	var out struct {
		Processing bool `json:"processing"`
	}

	if err := c.getJSON(ctx, "token status", "/api/v1/event/token/"+url.PathEscape(token), nil, &out); err != nil {
		return false, err
	}

	return out.Processing, nil
}

// ProjectUUID resolves a project by name and version (ErrNotFound when absent).
func (c *Client) ProjectUUID(ctx context.Context, name, version string) (string, error) {
	q := url.Values{"name": {name}}
	if version != "" {
		q.Set("version", version)
	}

	var out struct {
		UUID string `json:"uuid"`
	}

	if err := c.getJSON(ctx, "project lookup", "/api/v1/project/lookup", q, &out); err != nil {
		return "", err
	}

	if out.UUID == "" {
		return "", ErrNotFound
	}

	return out.UUID, nil
}

// Finding is one vulnerability Dependency-Track found in a project's component.
type Finding struct {
	VulnID    string
	Source    string
	Aliases   []string
	Component string // purl, or name@version
	State     string // analysis state, "" when never analyzed
	Suppress  bool
}

type findingJSON struct {
	Component struct {
		PURL    string `json:"purl"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"component"`
	Vulnerability struct {
		VulnID  string                       `json:"vulnId"`
		Source  string                       `json:"source"`
		Aliases []map[string]json.RawMessage `json:"aliases"`
	} `json:"vulnerability"`
	Analysis struct {
		State        string `json:"state"`
		IsSuppressed bool   `json:"isSuppressed"`
	} `json:"analysis"`
}

func (f *findingJSON) finding() Finding {
	out := Finding{
		VulnID: f.Vulnerability.VulnID, Source: f.Vulnerability.Source,
		State: f.Analysis.State, Suppress: f.Analysis.IsSuppressed, Component: f.Component.PURL,
	}

	if out.Component == "" {
		out.Component = f.Component.Name + "@" + f.Component.Version
	}

	for _, a := range f.Vulnerability.Aliases {
		for _, raw := range a {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				out.Aliases = append(out.Aliases, s)
			}
		}
	}

	return out
}

// Findings lists the project's findings, suppressed ones included.
func (c *Client) Findings(ctx context.Context, projectUUID string) ([]Finding, error) {
	var raw []findingJSON

	err := c.getJSON(ctx, "findings", "/api/v1/finding/project/"+url.PathEscape(projectUUID), url.Values{"suppressed": {"true"}}, &raw)
	if err != nil {
		return nil, err
	}

	out := make([]Finding, 0, len(raw))

	for i := range raw {
		out = append(out, raw[i].finding())
	}

	return out, nil
}

// Info is the outcome of a connection test.
type Info struct {
	Version            string   `json:"version"`
	Permissions        []string `json:"permissions"`
	MissingPermissions []string `json:"missing_permissions"`
}

// Check verifies the server answers and the API key is accepted, and reports
// the key's permissions against the ones the integration needs.
func (c *Client) Check(ctx context.Context, needVEX bool) (*Info, error) {
	var ver struct {
		Version string `json:"version"`
	}

	if err := c.getJSON(ctx, "version", "/api/version", nil, &ver); err != nil {
		return nil, err
	}

	var team struct {
		Permissions []struct {
			Name string `json:"name"`
		} `json:"permissions"`
	}

	if err := c.getJSON(ctx, "team", "/api/v1/team/self", nil, &team); err != nil {
		return nil, err
	}

	info := &Info{Version: ver.Version, Permissions: []string{}, MissingPermissions: []string{}}
	have := map[string]bool{}

	for _, p := range team.Permissions {
		info.Permissions = append(info.Permissions, p.Name)
		have[p.Name] = true
	}

	need := []string{PermBOMUpload, PermProjectCreation, PermViewPortfolio}
	if needVEX {
		need = append(need, PermViewVuln, PermVulnAnalysis)
	}

	for _, n := range need {
		if !have[n] && !have["PORTFOLIO_MANAGEMENT"] {
			info.MissingPermissions = append(info.MissingPermissions, n)
		}
	}

	return info, nil
}
