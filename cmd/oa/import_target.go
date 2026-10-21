// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Header names are case-insensitive; these are the canonical spellings of
	// X-OA-Import-Created-At and X-OA-Artifact-Id.
	headerImportCreatedAt = "X-Oa-Import-Created-At"
	headerArtifactID      = "X-Oa-Artifact-Id"
	indexPageSize         = 100
)

var (
	errTargetRepoMissing  = errors.New("target repository does not exist (create it first or pass --create-repos)")
	errTargetRepoFormat   = errors.New("target repository has a different format")
	errTargetRepoNotHosts = errors.New("target repository is not a hosted repository")
	errUploadConflict     = errors.New("package already exists with different content")
)

// importTarget talks to the destination OpenArtifact server.
type importTarget struct {
	c *Client
}

// request sends a request to the server. length < 0 means "unknown" (chunked).
func (t *importTarget) request(ctx context.Context, method, apiPath string, body io.Reader, length int64, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.c.baseURL+apiPath, body)
	if err != nil {
		return nil, err
	}

	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	if length >= 0 && body != nil {
		req.ContentLength = length
	}

	if t.c.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.c.token)
	}

	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	return t.c.httpClient.Do(req)
}

func (t *importTarget) getJSON(ctx context.Context, apiPath string, v any) (int, error) {
	resp, err := t.request(ctx, http.MethodGet, apiPath, nil, -1, nil)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, serverError(resp, "GET "+apiPath)
	}

	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(v)
}

// serverError turns an error response into an error carrying the server's
// message.
func serverError(resp *http.Response, what string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))

	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}

	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &e) == nil {
		if e.Message != "" {
			msg = e.Message
		} else if e.Error != "" {
			msg = e.Error
		}
	}

	return newStatusError(resp, what, msg)
}

// ensureRepo checks that the destination repository exists with the right
// format and is hosted, creating it when asked to. It reports whether the
// repository exists afterwards (false only for a dry run that would create it).
func (t *importTarget) ensureRepo(ctx context.Context, name string, f importFormat, create, dryRun bool) (bool, error) {
	var repo struct {
		Format   string `json:"format"`
		RepoType string `json:"repo_type"`
	}

	status, err := t.getJSON(ctx, "/api/v1/repository/"+url.PathEscape(name), &repo)

	switch {
	case err == nil:
		if repo.Format != string(f) {
			return false, fmt.Errorf("%w: %q is %s, source is %s", errTargetRepoFormat, name, repo.Format, f)
		}

		if repo.RepoType != "" && repo.RepoType != "hosted" {
			return false, fmt.Errorf("%w: %q is %s", errTargetRepoNotHosts, name, repo.RepoType)
		}

		return true, nil
	case status != http.StatusNotFound:
		return false, err
	case !create:
		return false, errTargetRepoMissing
	case dryRun:
		return false, nil
	}

	return true, t.createRepo(ctx, name, f)
}

func (t *importTarget) createRepo(ctx context.Context, name string, f importFormat) error {
	body, err := json.Marshal(map[string]any{
		"name":        name,
		"format":      string(f),
		"repo_type":   "hosted",
		"description": "Imported by oa import",
	})
	if err != nil {
		return err
	}

	resp, err := t.request(ctx, http.MethodPost, "/api/v1/repositories", strings.NewReader(string(body)), int64(len(body)),
		http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return serverError(resp, "creating repository "+name)
	}

	return nil
}

// indexEntry is one package already stored in the destination repository.
type indexEntry struct {
	ID     string
	SHA256 string
}

// targetIndex lets the importer recognise what is already present. Maven and
// raw packages are addressed by path; deb and rpm by content digest (the server
// derives their file names from package metadata, not from the source path).
type targetIndex struct {
	format importFormat
	byPath map[string]indexEntry
	bySHA  map[string]indexEntry
}

func (ix *targetIndex) lookup(p, sha string) (indexEntry, bool) {
	if ix == nil {
		return indexEntry{}, false
	}

	switch ix.format {
	case formatMaven, formatRaw:
		e, ok := ix.byPath[p]
		return e, ok && sha != "" && e.SHA256 == sha
	case formatDeb, formatRPM:
		e, ok := ix.bySHA[sha]
		return e, ok && sha != ""
	}

	return indexEntry{}, false
}

// eachPackage pages through the repository's package listing until fn returns
// false or the listing ends.
func (t *importTarget) eachPackage(ctx context.Context, name string, f importFormat, fn func(id, sha, filename string) bool) error {
	for page, seen := 1, 0; ; page++ {
		var resp struct {
			Total    int `json:"total"`
			Packages []struct {
				ID       string `json:"id"`
				SHA256   string `json:"sha256"`
				Filename string `json:"filename"`
			} `json:"packages"`
		}

		q := "?format=" + url.QueryEscape(string(f)) + "&page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(indexPageSize)
		if _, err := t.getJSON(ctx, "/api/v1/repositories/"+url.PathEscape(name)+"/packages"+q, &resp); err != nil {
			return fmt.Errorf("listing existing packages of %q: %w", name, err)
		}

		for _, p := range resp.Packages {
			if !fn(p.ID, p.SHA256, p.Filename) {
				return nil
			}
		}

		seen += len(resp.Packages)
		if len(resp.Packages) == 0 || seen >= resp.Total {
			return nil
		}
	}
}

// loadIndex pages through the repository's package listing.
func (t *importTarget) loadIndex(ctx context.Context, name string, f importFormat) (*targetIndex, error) {
	ix := &targetIndex{format: f, byPath: map[string]indexEntry{}, bySHA: map[string]indexEntry{}}

	err := t.eachPackage(ctx, name, f, func(id, sha, filename string) bool {
		e := indexEntry{ID: id, SHA256: sha}
		ix.byPath[filename] = e
		ix.bySHA[sha] = e

		return true
	})
	if err != nil {
		return nil, err
	}

	return ix, nil
}

// findBySHA looks for a package with the given content digest in the
// repository, as a fresh read of the server's state.
func (t *importTarget) findBySHA(ctx context.Context, name string, f importFormat, sha string) (indexEntry, bool, error) {
	var (
		found indexEntry
		ok    bool
	)

	err := t.eachPackage(ctx, name, f, func(id, s, _ string) bool {
		if s == sha {
			found, ok = indexEntry{ID: id, SHA256: s}, true
		}

		return !ok
	})

	return found, ok, err
}

// uploadMeta is what travels with an asset besides its bytes.
type uploadMeta struct {
	Created      time.Time
	DebDist      string
	DebComponent string
}

// uploadResult is what the server reported for a stored asset.
type uploadResult struct {
	ID string
	// SHA256 is the digest the server computed over what it received ("" when
	// the endpoint does not report one; see serverSHA256).
	SHA256 string
}

func (m uploadMeta) header() http.Header {
	h := http.Header{}
	if !m.Created.IsZero() {
		h.Set(headerImportCreatedAt, m.Created.UTC().Format(time.RFC3339))
	}

	return h
}

// upload streams body to the destination using the format's native upload
// endpoint.
func (t *importTarget) upload(ctx context.Context, f importFormat, dst string, a sourceAsset, body io.Reader, m uploadMeta) (uploadResult, error) {
	switch f {
	case formatMaven:
		return t.putFile(ctx, "/repos/maven/"+url.PathEscape(dst)+"/"+escapePath(a.Path), a, body, m, false)
	case formatRaw:
		return t.putFile(ctx, "/repos/raw/"+url.PathEscape(dst)+"/"+escapePath(a.Path), a, body, m, true)
	case formatDeb:
		fields := [][2]string{{"distribution", m.DebDist}, {"component", m.DebComponent}}
		return t.postPackage(ctx, "/api/v1/repositories/"+url.PathEscape(dst)+"/debs", a, body, m, fields)
	case formatRPM:
		return t.postPackage(ctx, "/api/v1/repositories/"+url.PathEscape(dst)+"/packages", a, body, m, nil)
	}

	return uploadResult{}, fmt.Errorf("%w: unsupported format %q", errImportUsage, f)
}

func (t *importTarget) putFile(ctx context.Context, apiPath string, a sourceAsset, body io.Reader, m uploadMeta, jsonReply bool) (uploadResult, error) {
	hdr := m.header()

	ct := a.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}

	hdr.Set("Content-Type", ct)

	length := a.Size
	if length <= 0 {
		length = -1
	}

	resp, err := t.request(ctx, http.MethodPut, apiPath, body, length, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return uploadResult{}, serverError(resp, "PUT "+apiPath)
	}

	res := uploadResult{ID: resp.Header.Get(headerArtifactID)}

	if jsonReply {
		var out struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return uploadResult{}, fmt.Errorf("PUT %s: decoding response: %w", apiPath, err)
		}

		res.ID, res.SHA256 = out.ID, strings.ToLower(out.SHA256)
	}

	return res, nil
}

// multipartStreamFields is multipartStream with plain form fields written
// before the file part.
func multipartStreamFields(fields [][2]string, file io.Reader, fieldName, fileName string) (io.ReadCloser, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		var err error

		for _, f := range fields {
			if f[1] == "" {
				continue
			}

			if err = mw.WriteField(f[0], f[1]); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}

		part, err := mw.CreateFormFile(fieldName, fileName)
		if err == nil {
			_, err = io.Copy(part, file)
		}

		if err == nil {
			err = mw.Close()
		}

		_ = pw.CloseWithError(err)
	}()

	return pr, mw.FormDataContentType()
}

func (t *importTarget) postPackage(ctx context.Context, apiPath string, a sourceAsset, body io.Reader, m uploadMeta, fields [][2]string) (uploadResult, error) {
	name := a.Path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}

	pr, contentType := multipartStreamFields(fields, body, "file", name)
	defer func() { _ = pr.Close() }()

	hdr := m.header()
	hdr.Set("Content-Type", contentType)

	resp, err := t.request(ctx, http.MethodPost, apiPath, pr, -1, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusConflict {
		return uploadResult{}, errUploadConflict
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return uploadResult{}, serverError(resp, "POST "+apiPath)
	}

	var out struct {
		Data struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return uploadResult{}, fmt.Errorf("POST %s: decoding response: %w", apiPath, err)
	}

	return uploadResult{ID: out.Data.ID, SHA256: strings.ToLower(out.Data.SHA256)}, nil
}

// serverSHA256 returns the digest the server stored for a Maven file (the
// upload response carries none) via the .sha256 sidecar it generates.
func (t *importTarget) serverSHA256(ctx context.Context, dst, p string) (string, error) {
	apiPath := "/repos/maven/" + url.PathEscape(dst) + "/" + escapePath(p) + ".sha256"

	resp, err := t.request(ctx, http.MethodGet, apiPath, nil, -1, nil)
	if err != nil {
		return "", err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", serverError(resp, "GET "+apiPath)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	if err != nil {
		return "", err
	}

	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "", fmt.Errorf("%w: GET %s: empty checksum", errHTTP, apiPath)
	}

	return strings.ToLower(fields[0]), nil
}

// remove best-effort deletes a stored asset (used when verification fails so a
// corrupt copy is not left behind).
func (t *importTarget) remove(ctx context.Context, f importFormat, dst, p, id string) error {
	var apiPath string

	switch f {
	case formatMaven:
		apiPath = "/repos/maven/" + url.PathEscape(dst) + "/" + escapePath(p)
	case formatRaw:
		apiPath = "/repos/raw/" + url.PathEscape(dst) + "/" + escapePath(p)
	case formatDeb:
		apiPath = "/api/v1/repositories/" + url.PathEscape(dst) + "/debs/" + url.PathEscape(id)
	case formatRPM:
		apiPath = "/api/v1/repositories/" + url.PathEscape(dst) + "/packages/" + url.PathEscape(id)
	}

	if apiPath == "" || ((f == formatDeb || f == formatRPM) && id == "") {
		return fmt.Errorf("%w: cannot determine what to remove", errImportUsage)
	}

	resp, err := t.request(ctx, http.MethodDelete, apiPath, nil, -1, nil)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return serverError(resp, "DELETE "+apiPath)
	}

	return nil
}

// setProperties merges props onto the package with the given id.
func (t *importTarget) setProperties(ctx context.Context, dst, id string, props map[string]string) error {
	body, err := json.Marshal(props)
	if err != nil {
		return err
	}

	apiPath := "/api/v1/repositories/" + url.PathEscape(dst) + "/artifacts/" + url.PathEscape(id) + "/properties"

	resp, err := t.request(ctx, http.MethodPatch, apiPath, strings.NewReader(string(body)), int64(len(body)),
		http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return serverError(resp, "PATCH "+apiPath)
	}

	return nil
}
