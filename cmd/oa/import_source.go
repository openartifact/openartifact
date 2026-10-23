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
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// importFormat is an OpenArtifact repository format the importer can migrate.
type importFormat string

const (
	formatMaven    importFormat = "maven"
	formatDeb      importFormat = "deb"
	formatRPM      importFormat = "rpm"
	formatRaw      importFormat = "raw"
	formatNPM      importFormat = "npm"
	formatPyPI     importFormat = "pypi"
	formatDocker   importFormat = "docker"
	formatHelm     importFormat = "helm"
	formatNuGet    importFormat = "nuget"
	formatRubyGems importFormat = "rubygems"
	formatCargo    importFormat = "cargo"
	formatGo       importFormat = "go"
	formatConda    importFormat = "conda"
	formatConan    importFormat = "conan"
	formatIvy      importFormat = "ivy"
	formatSBT      importFormat = "sbt"
)

const (
	listingAQL     = "aql"
	listingStorage = "storage"

	aqlPageSize = 1000
	maxErrBody  = 512
	// maxSourceJSONBytes caps a JSON response read from an import source.
	maxSourceJSONBytes = 64 << 20
)

var (
	errHTTP               = errors.New("unexpected response")
	errSourceRepoNotFound = errors.New("source repository not found")
	errSourceNotHosted    = errors.New("only hosted (local) repositories can be imported")
	errSourceFormat       = errors.New("unsupported source repository format (see docs/migration/nexus-artifactory.md for the supported formats)")
)

// sourceRepo describes a repository on the source system.
type sourceRepo struct {
	Name   string
	Format importFormat
	// DebDistribution/DebComponent come from the source repository's own
	// configuration when it has one (Nexus apt hosted: distribution).
	DebDistribution string
	DebComponent    string
}

// sourceAsset is one stored file on the source system.
type sourceAsset struct {
	Repo string
	// Path is relative to the repository root, without a leading slash.
	Path        string
	URL         string
	Size        int64
	SHA256      string
	SHA1        string
	Created     time.Time
	ContentType string
	// Props holds the source system's properties (Artifactory); a key may carry
	// several values.
	Props map[string][]string
	// Component is the package identity the source reported for this file
	// (Nexus components, Artifactory package properties); empty fields are
	// unknown and formats fall back to parsing Path.
	Component sourceComponent
}

// sourceComponent is the package a stored file belongs to, as the source
// system names it.
type sourceComponent struct {
	Group   string
	Name    string
	Version string
}

// importSource lists and streams the assets of a source system.
type importSource interface {
	// Repository resolves a repository by name and validates that it can be
	// imported.
	Repository(ctx context.Context, name string) (sourceRepo, error)
	// Assets lists every asset of the repository.
	Assets(ctx context.Context, repo sourceRepo) ([]sourceAsset, error)
	// Details fills in what the listing did not carry (creation time,
	// properties). It is only called for assets that are about to be written.
	Details(ctx context.Context, a *sourceAsset) error
	// Open streams the asset's content.
	Open(ctx context.Context, a sourceAsset) (io.ReadCloser, error)
}

// sourceHTTP issues authenticated GETs against the source system. Credentials
// are only ever attached to requests for the source's own origin.
type sourceHTTP struct {
	base   *url.URL
	user   string
	token  string
	client *http.Client
	// retry re-runs metadata reads after transient failures. Downloads are
	// not retried here: a failure can strike mid-stream, so the importer
	// re-opens the asset as part of retrying the whole transfer.
	retry *retrier
}

func newSourceHTTP(base *url.URL, user, token string, client *http.Client) *sourceHTTP {
	return &sourceHTTP{base: base, user: user, token: token, client: client}
}

func (s *sourceHTTP) sameOrigin(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, s.base.Scheme) && strings.EqualFold(u.Host, s.base.Host)
}

// resolve joins an API path (already escaped) onto the base URL.
func (s *sourceHTTP) resolve(escapedPath string) string {
	return strings.TrimRight(s.base.String(), "/") + escapedPath
}

func (s *sourceHTTP) do(ctx context.Context, method, rawURL string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	if s.sameOrigin(req.URL) {
		switch {
		case s.user != "":
			req.SetBasicAuth(s.user, s.token)
		case s.token != "":
			req.Header.Set("Authorization", "Bearer "+s.token)
		}
	}

	return s.client.Do(req)
}

// statusError builds an error from a non-2xx response without echoing more
// than a short slice of the body.
func statusError(resp *http.Response, what string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))

	return newStatusError(resp, what, strings.TrimSpace(string(snippet)))
}

// getJSON GETs rawURL and decodes a 200 response into v. A 404 yields
// errNotFound so callers can treat absence as data.
func (s *sourceHTTP) getJSON(ctx context.Context, rawURL string, v any) error {
	return s.retry.do(ctx, "GET "+redactURL(rawURL), func(int) error { return s.getJSONOnce(ctx, rawURL, v) })
}

func (s *sourceHTTP) getJSONOnce(ctx context.Context, rawURL string, v any) error {
	resp, err := s.do(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}

	if resp.StatusCode != http.StatusOK {
		return statusError(resp, "GET "+redactURL(rawURL))
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSourceJSONBytes)).Decode(v); err != nil {
		return fmt.Errorf("decoding %s: %w", redactURL(rawURL), err)
	}

	return nil
}

var errNotFound = errors.New("not found")

// redactURL drops the query string (continuation tokens) from log output.
func redactURL(raw string) string {
	base, _, _ := strings.Cut(raw, "?")

	return base
}

func (s *sourceHTTP) open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	resp, err := s.do(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()

		return nil, statusError(resp, "download "+redactURL(rawURL))
	}

	return resp.Body, nil
}

// escapePath percent-encodes each segment of a slash-separated path.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}

	return strings.Join(parts, "/")
}

// parseSourceTime parses the RFC 3339 timestamps both systems emit; an empty or
// malformed value yields the zero time.
func parseSourceTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}

	return t.UTC()
}

// ─── Nexus 3 ──────────────────────────────────────────────────────────────────

type nexusSource struct {
	http *sourceHTTP
}

func nexusFormat(f string) (importFormat, bool) {
	return lookupSourceFormat(nexusFormats, f)
}

func (n *nexusSource) Repository(ctx context.Context, name string) (sourceRepo, error) {
	var repos []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
		Type   string `json:"type"`
	}

	if err := n.http.getJSON(ctx, n.http.resolve("/service/rest/v1/repositories"), &repos); err != nil {
		return sourceRepo{}, fmt.Errorf("listing Nexus repositories: %w", err)
	}

	for _, r := range repos {
		if r.Name != name {
			continue
		}

		if r.Type != "hosted" {
			return sourceRepo{}, fmt.Errorf("%w: %q is %s", errSourceNotHosted, name, r.Type)
		}

		f, ok := nexusFormat(r.Format)
		if !ok {
			return sourceRepo{}, fmt.Errorf("%w: %q is %s", errSourceFormat, name, r.Format)
		}

		repo := sourceRepo{Name: name, Format: f}
		if f == formatDeb {
			repo.DebDistribution = n.aptDistribution(ctx, name)
		}

		return repo, nil
	}

	return sourceRepo{}, fmt.Errorf("%w: %q", errSourceRepoNotFound, name)
}

// aptDistribution reads the distribution from the repository configuration. The
// endpoint needs elevated rights on most installs, so any failure just means
// "unknown" and the importer falls back to the layout and the flags.
func (n *nexusSource) aptDistribution(ctx context.Context, name string) string {
	var cfg struct {
		Apt struct {
			Distribution string `json:"distribution"`
		} `json:"apt"`
	}

	if err := n.http.getJSON(ctx, n.http.resolve("/service/rest/v1/repositories/apt/hosted/"+url.PathEscape(name)), &cfg); err != nil {
		return ""
	}

	return cfg.Apt.Distribution
}

type nexusAsset struct {
	DownloadURL string `json:"downloadUrl"`
	Path        string `json:"path"`
	Checksum    struct {
		SHA1   string `json:"sha1"`
		SHA256 string `json:"sha256"`
	} `json:"checksum"`
	ContentType  string `json:"contentType"`
	LastModified string `json:"lastModified"`
	BlobCreated  string `json:"blobCreated"`
	FileSize     int64  `json:"fileSize"`
}

func (n *nexusSource) Assets(ctx context.Context, repo sourceRepo) ([]sourceAsset, error) {
	var (
		out   []sourceAsset
		token string
	)

	for {
		q := "?repository=" + url.QueryEscape(repo.Name)
		if token != "" {
			q += "&continuationToken=" + url.QueryEscape(token)
		}

		var page struct {
			Items             []nexusAsset `json:"items"`
			ContinuationToken string       `json:"continuationToken"`
		}

		if err := n.http.getJSON(ctx, n.http.resolve("/service/rest/v1/assets"+q), &page); err != nil {
			return nil, fmt.Errorf("listing assets of %q: %w", repo.Name, err)
		}

		for _, it := range page.Items {
			out = append(out, n.asset(repo, it))
		}

		if page.ContinuationToken == "" {
			break
		}

		token = page.ContinuationToken
	}

	if def, ok := formatDefs[repo.Format]; ok && def.components {
		if err := n.attachComponents(ctx, repo, out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// attachComponents fills in the package identity (group, name, version) of
// every asset that belongs to a Nexus component. Assets without a component
// (indexes, packuments) keep an empty identity.
func (n *nexusSource) attachComponents(ctx context.Context, repo sourceRepo, assets []sourceAsset) error {
	byPath := make(map[string]int, len(assets))
	for i, a := range assets {
		byPath[a.Path] = i
	}

	token := ""

	for {
		q := "?repository=" + url.QueryEscape(repo.Name)
		if token != "" {
			q += "&continuationToken=" + url.QueryEscape(token)
		}

		var page struct {
			Items []struct {
				Group   string       `json:"group"`
				Name    string       `json:"name"`
				Version string       `json:"version"`
				Assets  []nexusAsset `json:"assets"`
			} `json:"items"`
			ContinuationToken string `json:"continuationToken"`
		}

		if err := n.http.getJSON(ctx, n.http.resolve("/service/rest/v1/components"+q), &page); err != nil {
			return fmt.Errorf("listing components of %q: %w", repo.Name, err)
		}

		for _, c := range page.Items {
			for _, ca := range c.Assets {
				if i, ok := byPath[strings.TrimPrefix(ca.Path, "/")]; ok {
					assets[i].Component = sourceComponent{Group: c.Group, Name: c.Name, Version: c.Version}
				}
			}
		}

		if page.ContinuationToken == "" {
			return nil
		}

		token = page.ContinuationToken
	}
}

func (n *nexusSource) asset(repo sourceRepo, it nexusAsset) sourceAsset {
	p := strings.TrimPrefix(it.Path, "/")

	dl := it.DownloadURL
	if u, err := url.Parse(dl); err != nil || !n.http.sameOrigin(u) {
		// The advertised host is not the one we were pointed at (reverse proxy,
		// internal base URL): rebuild the URL from what we were given.
		dl = n.http.resolve("/repository/" + url.PathEscape(repo.Name) + "/" + escapePath(p))
	}

	// blobCreated is when the content first arrived; lastModified moves with
	// every metadata touch, so it is only a fallback.
	created := parseSourceTime(it.BlobCreated)
	if created.IsZero() {
		created = parseSourceTime(it.LastModified)
	}

	return sourceAsset{
		Repo:        repo.Name,
		Path:        p,
		URL:         dl,
		Size:        it.FileSize,
		SHA256:      strings.ToLower(it.Checksum.SHA256),
		SHA1:        strings.ToLower(it.Checksum.SHA1),
		Created:     created,
		ContentType: it.ContentType,
	}
}

func (n *nexusSource) Details(context.Context, *sourceAsset) error { return nil }

func (n *nexusSource) Open(ctx context.Context, a sourceAsset) (io.ReadCloser, error) {
	return n.http.open(ctx, a.URL)
}

// ─── JFrog Artifactory ────────────────────────────────────────────────────────

type artifactorySource struct {
	http *sourceHTTP
	// listing selects how files are enumerated: AQL (one query, carries
	// creation time and properties) or the storage API (list + per-file calls).
	listing string
}

func artifactoryFormat(packageType string) (importFormat, bool) {
	return lookupSourceFormat(artifactoryFormats, strings.ToLower(packageType))
}

func (a *artifactorySource) Repository(ctx context.Context, name string) (sourceRepo, error) {
	var repos []struct {
		Key         string `json:"key"`
		Type        string `json:"type"`
		PackageType string `json:"packageType"`
	}

	if err := a.http.getJSON(ctx, a.http.resolve("/api/repositories"), &repos); err != nil {
		return sourceRepo{}, fmt.Errorf("listing Artifactory repositories: %w", err)
	}

	for _, r := range repos {
		if r.Key != name {
			continue
		}

		if t := strings.ToUpper(r.Type); t != "LOCAL" && t != "FEDERATED" {
			return sourceRepo{}, fmt.Errorf("%w: %q is %s", errSourceNotHosted, name, strings.ToLower(r.Type))
		}

		f, ok := artifactoryFormat(r.PackageType)
		if !ok {
			return sourceRepo{}, fmt.Errorf("%w: %q is %s", errSourceFormat, name, r.PackageType)
		}

		return sourceRepo{Name: name, Format: f}, nil
	}

	return sourceRepo{}, fmt.Errorf("%w: %q", errSourceRepoNotFound, name)
}

func (a *artifactorySource) Assets(ctx context.Context, repo sourceRepo) ([]sourceAsset, error) {
	if a.listing == listingStorage {
		return a.listStorage(ctx, repo)
	}

	return a.listAQL(ctx, repo)
}

type aqlItem struct {
	Repo       string `json:"repo"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Created    string `json:"created"`
	Modified   string `json:"modified"`
	SHA256     string `json:"sha256"`
	ActualSHA1 string `json:"actual_sha1"`
	Properties []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"properties"`
}

// aqlQuery pages through a repository. The sort keeps pages stable while the
// source changes; Artifactory OSS rejects it, so listAQL falls back to the
// unsorted query (the server's native order).
func aqlQuery(repo string, offset int, sorted bool) string {
	order := ""
	if sorted {
		order = `.sort({"$asc":["path","name"]})`
	}

	return fmt.Sprintf(
		`items.find({"repo":%s,"type":"file"}).include("repo","path","name","size","created","modified","sha256","actual_sha1","property.*")%s.offset(%d).limit(%d)`,
		strconv.Quote(repo), order, offset, aqlPageSize)
}

// errAQLUnsorted is Artifactory OSS refusing the sort clause.
func errAQLUnsorted(err error) bool {
	var se *httpStatusError

	return errors.As(err, &se) && se.status == http.StatusBadRequest &&
		strings.Contains(se.msg, "Sorting is not supported")
}

func (a *artifactorySource) listAQL(ctx context.Context, repo sourceRepo) ([]sourceAsset, error) {
	var out []sourceAsset

	sorted := true

	for offset := 0; ; offset += aqlPageSize {
		page, err := a.aqlPage(ctx, repo.Name, offset, sorted)
		if sorted && errAQLUnsorted(err) {
			sorted = false
			page, err = a.aqlPage(ctx, repo.Name, offset, sorted)
		}

		if err != nil {
			return nil, err
		}

		for _, it := range page.Results {
			out = append(out, a.aqlAsset(repo, it))
		}

		if len(page.Results) < aqlPageSize {
			return out, nil
		}
	}
}

type aqlResponse struct {
	Results []aqlItem `json:"results"`
}

func (a *artifactorySource) aqlPage(ctx context.Context, repo string, offset int, sorted bool) (aqlResponse, error) {
	var page aqlResponse

	// The AQL POST only reads, so it is safe to repeat like a GET.
	err := a.http.retry.do(ctx, "AQL listing of "+strconv.Quote(repo), func(int) error {
		var err error

		page, err = a.aqlPageOnce(ctx, repo, offset, sorted)

		return err
	})

	return page, err
}

func (a *artifactorySource) aqlPageOnce(ctx context.Context, repo string, offset int, sorted bool) (aqlResponse, error) {
	what := "AQL listing of " + strconv.Quote(repo)

	resp, err := a.http.do(ctx, http.MethodPost, a.http.resolve("/api/search/aql"),
		strings.NewReader(aqlQuery(repo, offset, sorted)), "text/plain")
	if err != nil {
		return aqlResponse{}, fmt.Errorf("%s: %w", what, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return aqlResponse{}, statusError(resp, what)
	}

	var page aqlResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSourceJSONBytes)).Decode(&page); err != nil {
		return aqlResponse{}, fmt.Errorf("%s: decoding response: %w", what, err)
	}

	return page, nil
}

func (a *artifactorySource) aqlAsset(repo sourceRepo, it aqlItem) sourceAsset {
	p := it.Name
	if it.Path != "" && it.Path != "." {
		p = it.Path + "/" + it.Name
	}

	props := map[string][]string{}
	for _, kv := range it.Properties {
		props[kv.Key] = append(props[kv.Key], kv.Value)
	}

	created := parseSourceTime(it.Created)
	if created.IsZero() {
		created = parseSourceTime(it.Modified)
	}

	return sourceAsset{
		Repo:    repo.Name,
		Path:    p,
		URL:     a.http.resolve("/" + url.PathEscape(repo.Name) + "/" + escapePath(p)),
		Size:    it.Size,
		SHA256:  strings.ToLower(it.SHA256),
		SHA1:    strings.ToLower(it.ActualSHA1),
		Created: created,
		Props:   props,
	}
}

func (a *artifactorySource) listStorage(ctx context.Context, repo sourceRepo) ([]sourceAsset, error) {
	var list struct {
		Files []struct {
			URI          string `json:"uri"`
			Size         int64  `json:"size"`
			LastModified string `json:"lastModified"`
			Folder       bool   `json:"folder"`
			SHA1         string `json:"sha1"`
			SHA2         string `json:"sha2"`
		} `json:"files"`
	}

	u := a.http.resolve("/api/storage/" + url.PathEscape(repo.Name) + "?list&deep=1&listFolders=0&mdTimestamps=1")
	if err := a.http.getJSON(ctx, u, &list); err != nil {
		return nil, fmt.Errorf("listing files of %q: %w", repo.Name, err)
	}

	out := make([]sourceAsset, 0, len(list.Files))

	for _, f := range list.Files {
		if f.Folder {
			continue
		}

		p := strings.TrimPrefix(f.URI, "/")
		out = append(out, sourceAsset{
			Repo:    repo.Name,
			Path:    p,
			URL:     a.http.resolve("/" + url.PathEscape(repo.Name) + "/" + escapePath(p)),
			Size:    f.Size,
			SHA256:  strings.ToLower(f.SHA2),
			SHA1:    strings.ToLower(f.SHA1),
			Created: parseSourceTime(f.LastModified),
		})
	}

	return out, nil
}

// Details completes storage-API listings with the creation time and the
// properties; AQL listings already carry both.
func (a *artifactorySource) Details(ctx context.Context, asset *sourceAsset) error {
	if a.listing != listingStorage {
		return nil
	}

	base := a.http.resolve("/api/storage/" + url.PathEscape(asset.Repo) + "/" + escapePath(asset.Path))

	var info struct {
		Created   string `json:"created"`
		Checksums struct {
			SHA1   string `json:"sha1"`
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
		MimeType string `json:"mimeType"`
	}

	if err := a.http.getJSON(ctx, base, &info); err != nil {
		return fmt.Errorf("file info of %q: %w", asset.Path, err)
	}

	if t := parseSourceTime(info.Created); !t.IsZero() {
		asset.Created = t
	}

	if asset.SHA256 == "" {
		asset.SHA256 = strings.ToLower(info.Checksums.SHA256)
	}

	asset.ContentType = info.MimeType

	var props struct {
		Properties map[string][]string `json:"properties"`
	}

	switch err := a.http.getJSON(ctx, base+"?properties", &props); {
	case errors.Is(err, errNotFound):
		// no properties on this file
	case err != nil:
		return fmt.Errorf("properties of %q: %w", asset.Path, err)
	default:
		asset.Props = props.Properties
	}

	return nil
}

func (a *artifactorySource) Open(ctx context.Context, asset sourceAsset) (io.ReadCloser, error) {
	return a.http.open(ctx, asset.URL)
}

// ─── Selection and layout ─────────────────────────────────────────────────────

// importable reports whether an asset is a payload file the importer should
// move for the given format. Indexes, checksum sidecars and signatures are
// regenerated by the target.
func importable(f importFormat, p string) bool {
	// Artifactory keeps internal state in dot-directories (.index, .npm, ...).
	if strings.HasPrefix(p, ".") {
		return false
	}

	def, ok := formatDefs[f]

	return ok && def.importable(p)
}

func importableDeb(p string) bool {
	return !strings.HasPrefix(p, "dists/") && strings.HasSuffix(p, ".deb")
}

func importableRPM(p string) bool {
	if strings.HasPrefix(p, "repodata/") || strings.Contains(p, "/repodata/") {
		return false
	}

	return strings.HasSuffix(p, ".rpm")
}

func importableMaven(p string) bool {
	base := path.Base(p)

	for _, s := range []string{".md5", ".sha1", ".sha256", ".sha512"} {
		if strings.HasSuffix(base, s) {
			return false
		}
	}

	if base == "maven-metadata.xml" {
		return false
	}

	// group/artifact/version/file at the very least.
	if strings.Count(p, "/") < 3 {
		return false
	}

	return true
}

// deriveAptLayout infers the (single) distribution and component of a Nexus apt
// repository from its dists/<dist>/<component>/binary-<arch>/ index paths. An
// ambiguous layout yields "" for that dimension.
func deriveAptLayout(assets []sourceAsset) (string, string) {
	dists := map[string]struct{}{}
	comps := map[string]struct{}{}

	for _, a := range assets {
		parts := strings.Split(a.Path, "/")
		if len(parts) < 5 || parts[0] != "dists" || !strings.HasPrefix(parts[3], "binary-") {
			continue
		}

		dists[parts[1]] = struct{}{}
		comps[parts[2]] = struct{}{}
	}

	return onlyKey(dists), onlyKey(comps)
}

func onlyKey(m map[string]struct{}) string {
	if len(m) != 1 {
		return ""
	}

	for k := range m {
		return k
	}

	return ""
}

// ─── Properties ───────────────────────────────────────────────────────────────

const defaultPropertyPatterns = "deb.*,rpm.*,build.name,build.number,vcs.*"

// parsePropertyPatterns splits a comma-separated list of exact keys or
// "prefix*" patterns. "none" (or an empty list) selects nothing.
func parsePropertyPatterns(v string) []string {
	if strings.EqualFold(strings.TrimSpace(v), "none") {
		return nil
	}

	var out []string

	for p := range strings.SplitSeq(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}

func propertyMatches(patterns []string, key string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		} else if p == key {
			return true
		}
	}

	return false
}

// selectProperties returns the selected properties flattened to single string
// values (multi-valued properties are comma-joined).
func selectProperties(props map[string][]string, patterns []string) map[string]string {
	out := map[string]string{}

	for k, vs := range props {
		if propertyMatches(patterns, k) && len(vs) > 0 {
			out[k] = strings.Join(vs, ",")
		}
	}

	return out
}

// firstValue returns the first value of a (possibly comma-joined) property.
func firstValue(vs []string) string {
	if len(vs) == 0 {
		return ""
	}

	first, _, _ := strings.Cut(vs[0], ",")

	return strings.TrimSpace(first)
}
