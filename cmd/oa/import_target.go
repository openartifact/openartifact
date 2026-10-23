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

// send issues a request and turns an error status into an error: 409 becomes
// errUploadConflict, anything else outside 2xx a serverError. On success the
// caller owns (and closes) the response body.
func (t *importTarget) send(ctx context.Context, method, apiPath string, body io.Reader, length int64, hdr http.Header) (*http.Response, error) {
	resp, err := t.request(ctx, method, apiPath, body, length, hdr)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode/100 == 2 {
		return resp, nil
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusConflict {
		return nil, errUploadConflict
	}

	return nil, serverError(resp, method+" "+apiPath)
}

// storedResult resolves the id and stored digest of a package that was just
// uploaded, by finding it in the destination's listing (see findStored).
func (t *importTarget) storedResult(ctx context.Context, dst string, f importFormat, q string, want func(listedPackage) bool) (uploadResult, error) {
	p, ok, err := t.findStored(ctx, dst, f, q, want)
	if err != nil {
		return uploadResult{}, err
	}

	if !ok {
		return uploadResult{}, fmt.Errorf("%w: the server accepted the upload but the package is not listed in %q", errImportVerify, dst)
	}

	return uploadResult{ID: p.ID, SHA256: strings.ToLower(p.SHA256)}, nil
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

// listedPackage is one row of the destination's package listing.
type listedPackage struct {
	ID       string
	SHA256   string
	Filename string
	Name     string
	Version  string
	Arch     string
}

// targetIndex lets the importer recognise what is already present: by the
// listed file name (Maven, raw: the path is the address), by content digest
// (everything whose file names the server derives from package metadata), or
// by whatever a format's present function derives from the listing.
type targetIndex struct {
	format importFormat
	byPath map[string]indexEntry
	bySHA  map[string]indexEntry
	// all keeps every listed row for formats with their own identity rules.
	all []listedPackage
}

func (ix *targetIndex) lookup(a sourceAsset) (indexEntry, bool) {
	if ix == nil {
		return indexEntry{}, false
	}

	def := formatDefs[ix.format]
	if def.present != nil {
		return def.present(ix, a)
	}

	switch def.match {
	case matchPath:
		e, ok := ix.byPath[a.Path]
		return e, ok && a.SHA256 != "" && e.SHA256 == a.SHA256
	case matchSHA:
		e, ok := ix.bySHA[a.SHA256]
		return e, ok && a.SHA256 != ""
	}

	return indexEntry{}, false
}

// eachListed pages through the repository's package listing, optionally
// narrowed by a case-insensitive text q on name or version, until fn returns
// false or the listing ends.
func (t *importTarget) eachListed(ctx context.Context, name string, f importFormat, q string, fn func(listedPackage) bool) error {
	for page, seen := 1, 0; ; page++ {
		var resp struct {
			Total    int `json:"total"`
			Packages []struct {
				ID       string `json:"id"`
				SHA256   string `json:"sha256"`
				Filename string `json:"filename"`
				Name     string `json:"name"`
				Version  string `json:"version"`
				Arch     string `json:"arch"`
			} `json:"packages"`
		}

		query := "?format=" + url.QueryEscape(string(f)) + "&page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(indexPageSize)
		if q != "" {
			query += "&q=" + url.QueryEscape(q)
		}

		if _, err := t.getJSON(ctx, "/api/v1/repositories/"+url.PathEscape(name)+"/packages"+query, &resp); err != nil {
			return fmt.Errorf("listing existing packages of %q: %w", name, err)
		}

		for _, p := range resp.Packages {
			if !fn(listedPackage{ID: p.ID, SHA256: p.SHA256, Filename: p.Filename, Name: p.Name, Version: p.Version, Arch: p.Arch}) {
				return nil
			}
		}

		seen += len(resp.Packages)
		if len(resp.Packages) == 0 || seen >= resp.Total {
			return nil
		}
	}
}

// findStored looks in the destination for the package accepted by want, among
// the rows matching the text q. Upload functions use it to learn the id and the
// stored digest of a package whose upload response carries neither: want should
// match the package's identity (name, version, file name), not its digest, so
// that a copy stored with different bytes is detected by verification.
func (t *importTarget) findStored(ctx context.Context, dst string, f importFormat, q string, want func(listedPackage) bool) (listedPackage, bool, error) {
	var (
		found listedPackage
		ok    bool
	)

	err := t.eachListed(ctx, dst, f, q, func(p listedPackage) bool {
		if want(p) {
			found, ok = p, true
		}

		return !ok
	})

	return found, ok, err
}

// loadIndex pages through the repository's package listing.
func (t *importTarget) loadIndex(ctx context.Context, name string, f importFormat) (*targetIndex, error) {
	ix := &targetIndex{format: f, byPath: map[string]indexEntry{}, bySHA: map[string]indexEntry{}}

	err := t.eachListed(ctx, name, f, "", func(p listedPackage) bool {
		e := indexEntry{ID: p.ID, SHA256: p.SHA256}
		ix.byPath[p.Filename] = e
		ix.bySHA[p.SHA256] = e
		ix.all = append(ix.all, p)

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
	p, ok, err := t.findStored(ctx, name, f, "", func(p listedPackage) bool { return p.SHA256 == sha })

	return indexEntry{ID: p.ID, SHA256: p.SHA256}, ok, err
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
	def, ok := formatDefs[f]
	if !ok || def.upload == nil {
		return uploadResult{}, fmt.Errorf("%w: unsupported format %q", errImportUsage, f)
	}

	return def.upload(ctx, t, uploadRequest{Dst: dst, Asset: a, Body: body, Meta: m})
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
func (t *importTarget) remove(ctx context.Context, f importFormat, dst string, a sourceAsset, res uploadResult) error {
	def, ok := formatDefs[f]
	if !ok || def.remove == nil {
		return errNoRemove(f)
	}

	return def.remove(ctx, t, dst, a, res)
}

// deletePath issues a DELETE on a native format endpoint (already escaped).
func (t *importTarget) deletePath(ctx context.Context, apiPath string) error {
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

// deleteByID deletes a package by its id below an API prefix (ending in "/").
func (t *importTarget) deleteByID(ctx context.Context, prefix, id string) error {
	if id == "" {
		return fmt.Errorf("%w: cannot determine what to remove", errImportUsage)
	}

	return t.deletePath(ctx, prefix+url.PathEscape(id))
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
