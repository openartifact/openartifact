// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/format/terraform"
)

// Parsers of the documents a Terraform or OpenTofu registry serves and of the
// paths its clients request. They hold no state and are fuzzed.

// tfArchiveExts are the archive kinds a module may be fetched as (the formats
// Terraform's own getter unpacks from a plain HTTP URL) and the extension of a
// provider zip, which together are the files of a Terraform proxy that are not
// small documents.
//
//nolint:gochecknoglobals // read-only table
var tfArchiveExts = []string{".zip", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".tar"}

// tfArchiveKey reports whether key is a module archive or provider zip.
func tfArchiveKey(key string) bool {
	if !strings.HasPrefix(key, "modules/") && !strings.HasPrefix(key, "providers/") {
		return false
	}

	return slices.ContainsFunc(tfArchiveExts, func(ext string) bool { return strings.HasSuffix(key, ext) })
}

// tfProviderZipKey reports whether key is a provider platform zip.
func tfProviderZipKey(key string) bool {
	return strings.HasPrefix(key, "providers/") && strings.HasSuffix(key, ".zip")
}

const (
	// tfMaxGet bounds an X-Terraform-Get value.
	tfMaxGet = 4096
	// tfMaxSums bounds a SHA256SUMS file: one line per platform.
	tfMaxSums = 1 << 20
	// tfMaxDoc bounds the small upstream documents read into memory (discovery,
	// package documents, module download answers).
	tfMaxDoc = 4 << 20
)

// tfKind is the request a Terraform client made of a repository.
type tfKind int

const (
	tfProviderVersions tfKind = iota + 1
	tfProviderPackage
	tfModuleVersions
	tfModuleLatestDownload
	tfModuleDownload
	tfModuleVersion
	tfModuleLatest
)

// tfPath is a parsed request path, relative to the repository (the same paths
// the hosted registry serves).
type tfPath struct {
	kind    tfKind
	name    string // module name, or provider type
	system  string // module system
	version string
	os      string
	arch    string
}

// parseTerraformPath classifies a repository-relative request path. Every
// segment is validated the way the hosted registry validates its own, so the
// names are safe in upstream URLs and cache keys; nothing else is accepted.
func parseTerraformPath(rel string) (tfPath, bool) {
	segs := strings.Split(strings.TrimPrefix(rel, "/"), "/")

	switch len(segs) {
	case 2:
		if !terraform.ValidName(segs[0]) {
			return tfPath{}, false
		}

		switch {
		case segs[1] == "versions":
			return tfPath{kind: tfProviderVersions, name: segs[0]}, true
		case terraform.ValidName(segs[1]):
			return tfPath{kind: tfModuleLatest, name: segs[0], system: segs[1]}, true
		}
	case 3:
		return parseTerraformModulePath(segs)
	case 4:
		if validTFModule(segs[0], segs[1]) && terraform.ValidVersion(segs[2]) && segs[3] == "download" {
			return tfPath{kind: tfModuleDownload, name: segs[0], system: segs[1], version: segs[2]}, true
		}
	case 5:
		if terraform.ValidName(segs[0]) && terraform.ValidVersion(segs[1]) && segs[2] == "download" &&
			terraform.ValidPlatformPart(segs[3]) && terraform.ValidPlatformPart(segs[4]) {
			return tfPath{kind: tfProviderPackage, name: segs[0], version: segs[1], os: segs[3], arch: segs[4]}, true
		}
	}

	return tfPath{}, false
}

// parseTerraformModulePath classifies NAME/SYSTEM/{versions,download,VERSION}.
func parseTerraformModulePath(segs []string) (tfPath, bool) {
	if !validTFModule(segs[0], segs[1]) {
		return tfPath{}, false
	}

	p := tfPath{name: segs[0], system: segs[1]}

	switch {
	case segs[2] == "versions":
		p.kind = tfModuleVersions
	case segs[2] == "download":
		p.kind = tfModuleLatestDownload
	case terraform.ValidVersion(segs[2]):
		p.kind, p.version = tfModuleVersion, segs[2]
	default:
		return tfPath{}, false
	}

	return p, true
}

// validTFModule reports whether name/system is a module the registry routes
// can address: a system called "versions" would be the provider version list.
func validTFModule(name, system string) bool {
	return terraform.ValidName(name) && terraform.ValidName(system) && system != "versions"
}

// tfEndpoints are the service roots a registry's discovery document announces,
// absolute and ending in a slash. A nil root is a service the registry lacks.
type tfEndpoints struct {
	modules   *url.URL
	providers *url.URL
}

// parseTerraformDiscovery reads /.well-known/terraform.json. Service URLs may
// be relative to the document's URL. A registry that announces neither
// modules.v1 nor providers.v1 is not one.
func parseTerraformDiscovery(body []byte, docURL *url.URL) (tfEndpoints, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return tfEndpoints{}, fmt.Errorf("discovery document: %w", err)
	}

	resolve := func(key string) (*url.URL, error) {
		v, ok := raw[key]
		if !ok {
			return nil, nil //nolint:nilnil // an absent service is not an error
		}

		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, fmt.Errorf("discovery %s: %w", key, err)
		}

		ref, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("discovery %s: %w", key, err)
		}

		u := docURL.ResolveReference(ref)
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("discovery %s: %w: %q is not an http(s) service URL", key, errTFBadDocument, s)
		}

		if !strings.HasSuffix(u.EscapedPath(), "/") {
			u.Path += "/"
			u.RawPath = ""
		}

		return u, nil
	}

	var (
		ep  tfEndpoints
		err error
	)

	if ep.modules, err = resolve("modules.v1"); err != nil {
		return tfEndpoints{}, err
	}

	if ep.providers, err = resolve("providers.v1"); err != nil {
		return tfEndpoints{}, err
	}

	if ep.modules == nil && ep.providers == nil {
		return tfEndpoints{}, fmt.Errorf("%w: discovery document announces neither modules.v1 nor providers.v1", errTFBadDocument)
	}

	return ep, nil
}

// tfEndpointMemo remembers the endpoints parsed from a cached discovery
// document, keyed by the document's blob and URL (a blob never changes, so the
// memo never goes stale).
type tfEndpointMemo struct {
	mu sync.Mutex
	m  map[string]tfEndpoints
}

const tfEndpointMemoEntries = 64

func (c *tfEndpointMemo) get(key string) (tfEndpoints, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	v, ok := c.m[key]

	return v, ok
}

func (c *tfEndpointMemo) put(key string, ep tfEndpoints) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m == nil || len(c.m) >= tfEndpointMemoEntries {
		c.m = make(map[string]tfEndpoints)
	}

	c.m[key] = ep
}

// errTFUnsupportedSource marks a module source this proxy does not fetch.
var errTFUnsupportedSource = errors.New("unsupported module source")

// tfModuleSource is where a module's archive is fetched from.
type tfModuleSource struct {
	url    string // absolute http(s) URL, without the archive hint
	ext    string // archive kind: one of tfArchiveExts without the dot
	strip  bool   // the archive has one wrapper directory (a GitHub tarball) that is removed
	subdir string // the directory of the (stripped) archive that is the module, "" for all of it
}

// repack reports whether the archive is rewritten before it is cached.
func (s tfModuleSource) repack() bool { return s.strip || s.subdir != "" }

// tfCodeloadBase is where GitHub serves the tarball of a ref.
const tfCodeloadBase = "https://codeload.github.com"

var (
	tfGitHubOwnerRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	tfGitHubRepoRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	tfRefSegmentRE  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)
	tfSubdirSegRE   = regexp.MustCompile(`^[A-Za-z0-9._+@~-]{1,100}$`)
	tfDepthRE       = regexp.MustCompile(`^[0-9]{1,4}$`)
)

// validTFGitRef reports whether ref is a branch, tag or commit name that is
// safe to put in a codeload URL: slash-separated segments of letters, digits
// and ._+-, none starting with a dot or dash, no "..".
func validTFGitRef(ref string) bool {
	if ref == "" || len(ref) > 200 || strings.Contains(ref, "..") {
		return false
	}

	for seg := range strings.SplitSeq(ref, "/") {
		if !tfRefSegmentRE.MatchString(seg) {
			return false
		}
	}

	return true
}

// cleanTFSubdir validates the directory a source selects with "//": relative
// segments of a conservative character set, no empty, "." or ".." segment. A
// trailing slash is dropped.
func cleanTFSubdir(sub string) (string, bool) {
	sub = strings.TrimSuffix(sub, "/")
	if sub == "" || len(sub) > 400 {
		return "", false
	}

	for seg := range strings.SplitSeq(sub, "/") {
		if seg == "." || seg == ".." || !tfSubdirSegRE.MatchString(seg) {
			return "", false
		}
	}

	return sub, true
}

// splitModuleSubdir cuts the "//subdir" selector off a source: it follows the
// URL (or the "host/path" of a detector) and may be followed by the query, as
// in "https://github.com/o/r.git//modules/x?ref=v1". Only the path can carry
// the selector: a "//" after a "?" or "#" belongs to a query value or a
// fragment.
func splitModuleSubdir(raw string) (string, string, error) {
	pathEnd := len(raw)
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		pathEnd = i
	}

	start := 0
	if i := strings.Index(raw[:pathEnd], "://"); i >= 0 {
		start = i + len("://")
	}

	i := strings.Index(raw[start:pathEnd], "//")
	if i < 0 {
		return raw, "", nil
	}

	head, sub := raw[:start+i], raw[start+i+2:]

	if j := strings.IndexByte(sub, '?'); j >= 0 {
		head += "?" + sub[j+1:]
		sub = sub[:j]
	}

	if sub = strings.TrimSuffix(sub, "/"); sub == "" {
		return head, "", nil
	}

	clean, ok := cleanTFSubdir(sub)
	if !ok {
		return "", "", fmt.Errorf(
			"%w: %q selects a subdirectory that is not a clean relative path",
			errTFUnsupportedSource,
			truncate(raw, 80),
		)
	}

	return head, clean, nil
}

// parseGitHubSource maps a github.com git source ("git::https://github.com/o/r.git?ref=X",
// "https://github.com/o/r", "github.com/o/r") to the codeload tarball of its
// ref. ok is false for a source that is not a GitHub repository address, which
// the caller then reads as a plain archive URL (unless a git getter was forced).
func parseGitHubSource(head, forced, codeload string) (tfModuleSource, bool, error) {
	u := gitHubURL(head)
	if u == nil {
		return tfModuleSource{}, false, nil
	}

	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) != 2 && forced == "" {
		// A longer path is a file on github.com (a release archive), not a repository.
		return tfModuleSource{}, false, nil
	}

	repo, err := gitHubRepo(u, segs, head)
	if err != nil {
		return tfModuleSource{}, true, err
	}

	ref, err := gitHubRef(u.RawQuery, head)
	if err != nil {
		return tfModuleSource{}, true, err
	}

	return tfModuleSource{url: codeload + "/" + segs[0] + "/" + repo + "/tar.gz/" + ref, ext: "tar.gz", strip: true}, true, nil
}

// gitHubURL parses an http(s) address on github.com, with the bare
// "github.com/o/r" detector made absolute. It is nil for anything else.
func gitHubURL(head string) *url.URL {
	if _, bare := strings.CutPrefix(head, "github.com/"); bare {
		head = "https://" + head
	}

	u, err := url.Parse(head)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !strings.EqualFold(u.Host, "github.com") {
		return nil
	}

	return u
}

// gitHubRepo validates the owner and repository of a github.com address and
// returns the repository name without its ".git" suffix.
func gitHubRepo(u *url.URL, segs []string, head string) (string, error) {
	if u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Port() != "" {
		return "", fmt.Errorf("%w: %q is not a plain GitHub repository address", errTFUnsupportedSource, truncate(head, 80))
	}

	if len(segs) != 2 {
		return "", fmt.Errorf("%w: %q is not a GitHub repository address (owner/repository)", errTFUnsupportedSource, truncate(head, 80))
	}

	repo := strings.TrimSuffix(segs[1], ".git")
	if !tfGitHubOwnerRE.MatchString(segs[0]) || !tfGitHubRepoRE.MatchString(repo) || repo == "." || repo == ".." {
		return "", fmt.Errorf("%w: %q is not a GitHub repository address (owner/repository)", errTFUnsupportedSource, truncate(head, 80))
	}

	return repo, nil
}

// gitHubRef reads the ref of a git source query ("HEAD" without one). Only
// ref and depth (ignored: codeload serves whole tarballs) are accepted, each
// once.
func gitHubRef(rawQuery, head string) (string, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("%w: %q has a malformed query", errTFUnsupportedSource, truncate(head, 80))
	}

	ref := "HEAD"

	for name, values := range query {
		if len(values) != 1 {
			return "", fmt.Errorf("%w: the parameter %q is given more than once", errTFUnsupportedSource, truncate(name, 40))
		}

		switch name {
		case "ref":
			if !validTFGitRef(values[0]) {
				return "", fmt.Errorf(
					"%w: the git ref %q is not a plain branch, tag or commit name",
					errTFUnsupportedSource,
					truncate(values[0], 80),
				)
			}

			ref = values[0]
		case "depth":
			if !tfDepthRE.MatchString(values[0]) {
				return "", fmt.Errorf("%w: the depth %q is not a number", errTFUnsupportedSource, truncate(values[0], 20))
			}
		default:
			return "", fmt.Errorf("%w: the git parameter %q is not supported", errTFUnsupportedSource, truncate(name, 40))
		}
	}

	return ref, nil
}

// tfGetterParams are go-getter query parameters that change what is fetched or
// how it is checked; a source using one is not a plain archive URL.
var tfGetterParams = []string{"checksum", "ref", "depth", "sshkey", "filename"} //nolint:gochecknoglobals // read-only table

// parseModuleGet interprets the source a registry names for a module (the
// X-Terraform-Get header, or the location of the OpenTofu answer). base is the
// URL of the download answer that carried it, which relative sources are
// resolved against. Two kinds of source are accepted: github.com repositories
// ("git::https://github.com/o/r.git?ref=X", "https://github.com/o/r", the bare
// "github.com/o/r"), mapped to the codeload tarball of the ref under the
// codeload base URL, and plain archive URLs (http(s), a known archive kind, no
// checksum or other getter parameters). Either may select a subdirectory with
// "//subdir"; a subdirectory of an archive is supported for zip and tar.gz.
// Everything else (other getters and git hosts, scp-style addresses, ssh) is
// refused with errTFUnsupportedSource; an empty or oversized value is a plain
// error.
func parseModuleGet(raw string, base *url.URL, codeload string) (tfModuleSource, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > tfMaxGet {
		return tfModuleSource{}, fmt.Errorf("%w: module source of %d bytes", errTFBadDocument, len(raw))
	}

	forced := ""

	if i := strings.Index(raw, "::"); i > 0 && !strings.ContainsAny(raw[:i], "/?#") {
		if forced = raw[:i]; forced != "git" {
			return tfModuleSource{}, fmt.Errorf("%w: %s getter %q", errTFUnsupportedSource, forced, forced+"::")
		}

		raw = raw[i+2:]
	}

	raw, subdir, err := splitModuleSubdir(raw)
	if err != nil {
		return tfModuleSource{}, err
	}

	src, err := parsePlainOrGitHub(raw, forced, subdir, base, codeload)
	if err != nil {
		return tfModuleSource{}, err
	}

	if src.subdir != "" && !slices.Contains([]string{"zip", "tar.gz", "tgz"}, src.ext) {
		return tfModuleSource{}, fmt.Errorf(
			"%w: %q selects a subdirectory of a %s archive (zip and tar.gz only)",
			errTFUnsupportedSource,
			truncate(raw, 80),
			src.ext,
		)
	}

	return src, nil
}

// parsePlainOrGitHub is parseModuleGet after the getter prefix and the
// subdirectory selector are removed from raw.
func parsePlainOrGitHub(raw, forced, subdir string, base *url.URL, codeload string) (tfModuleSource, error) {
	gh, ok, err := parseGitHubSource(raw, forced, codeload)
	if err != nil {
		return tfModuleSource{}, err
	}

	if ok {
		gh.subdir = subdir

		return gh, nil
	}

	if forced != "" {
		return tfModuleSource{}, fmt.Errorf("%w: the git source %q is not on github.com", errTFUnsupportedSource, truncate(raw, 80))
	}

	ref, err := url.Parse(raw)
	if err != nil {
		// Not a URL: scp-style git sources ("git@host:org/repo") and the like.
		return tfModuleSource{}, fmt.Errorf("%w: %q is not a URL", errTFUnsupportedSource, truncate(raw, 80))
	}

	// A path-only source is relative to the download answer; a bare "host/path"
	// (a getter detector such as github.com/org/repo) has no scheme and no
	// leading slash and falls through to the scheme check.
	if ref.Scheme == "" && ref.Host == "" &&
		(strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../")) {
		ref = base.ResolveReference(ref)
	}

	if ref.Scheme != "http" && ref.Scheme != "https" {
		return tfModuleSource{}, fmt.Errorf("%w: %q is not an http(s) archive URL", errTFUnsupportedSource, raw)
	}

	if ref.Host == "" || ref.User != nil || ref.Fragment != "" {
		return tfModuleSource{}, fmt.Errorf("%w: %q is not a plain archive URL", errTFUnsupportedSource, raw)
	}

	// A "//" that splitModuleSubdir did not take as a selector (a relative
	// source such as "/0://x.zip") is not a plain path.
	if strings.Contains(ref.Path, "//") {
		return tfModuleSource{}, fmt.Errorf("%w: %q has an empty path segment", errTFUnsupportedSource, truncate(raw, 80))
	}

	hint, query, params := splitArchiveHint(ref.RawQuery)
	if slices.ContainsFunc(params, func(p string) bool { return slices.Contains(tfGetterParams, p) }) {
		return tfModuleSource{}, fmt.Errorf("%w: %q uses a getter parameter", errTFUnsupportedSource, raw)
	}

	ext := hint
	if ext == "" {
		ext = archiveExtOf(ref.Path)
	}

	if !slices.Contains(tfArchiveExts, "."+ext) {
		return tfModuleSource{}, fmt.Errorf("%w: %q is not a known archive kind", errTFUnsupportedSource, raw)
	}

	ref.RawQuery = query

	return tfModuleSource{url: ref.String(), ext: ext, subdir: subdir}, nil
}

// splitArchiveHint removes the go-getter "archive" parameter from a raw query,
// leaving the other pairs exactly as they were (a signed URL must not be
// re-encoded). It returns the hint, the remaining query and the parameter names.
func splitArchiveHint(rawQuery string) (string, string, []string) {
	if rawQuery == "" {
		return "", "", nil
	}

	var (
		hint  string
		kept  []string
		names []string
	)

	for pair := range strings.SplitSeq(rawQuery, "&") {
		if pair == "" {
			continue
		}

		name, value, _ := strings.Cut(pair, "=")
		if n, err := url.QueryUnescape(name); err == nil {
			name = n
		}

		names = append(names, name)

		if name == "archive" {
			if v, err := url.QueryUnescape(value); err == nil {
				value = v
			}

			hint = value

			continue
		}

		kept = append(kept, pair)
	}

	return hint, strings.Join(kept, "&"), names
}

// archiveExtOf returns the archive kind a URL path ends in ("" if none).
func archiveExtOf(p string) string {
	for _, ext := range tfArchiveExts {
		if strings.HasSuffix(strings.ToLower(p), ext) {
			return ext[1:]
		}
	}

	return ""
}

// tfFileNameRE is the shape of a file name taken from an upstream document and
// used in cache keys and download URLs.
var tfFileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$`)

// validTFFileName reports whether s is a safe file name.
func validTFFileName(s string) bool { return tfFileNameRE.MatchString(s) }

// parseSHA256SUMS reads a checksum file ("<64 hex>  <name>" per line, binary
// mode marked by a "*" before the name) into name -> lowercase digest. A
// malformed line, a repeated name with another digest, or an empty file is an
// error: it vouches for nothing.
func parseSHA256SUMS(data []byte) (map[string]string, error) {
	if len(data) > tfMaxSums {
		return nil, fmt.Errorf("%w: file too large", errTFBadChecksums)
	}

	sums := make(map[string]string)

	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}

		digest, name, ok := strings.Cut(line, " ")
		if !ok || len(digest) != sha256HexLen {
			return nil, fmt.Errorf("%w: malformed line %q", errTFBadChecksums, truncate(line, 80))
		}

		if _, err := hex.DecodeString(digest); err != nil {
			return nil, fmt.Errorf("%w: malformed digest %q", errTFBadChecksums, truncate(digest, 80))
		}

		name = strings.TrimPrefix(strings.TrimPrefix(name, " "), "*")
		if name == "" || strings.ContainsAny(name, "/\\\x00") {
			return nil, fmt.Errorf("%w: malformed file name %q", errTFBadChecksums, truncate(name, 80))
		}

		digest = strings.ToLower(digest)
		if prev, dup := sums[name]; dup && prev != digest {
			return nil, fmt.Errorf("%w: %q is listed twice with different digests", errTFBadChecksums, name)
		}

		sums[name] = digest
	}

	if len(sums) == 0 {
		return nil, fmt.Errorf("%w: file is empty", errTFBadChecksums)
	}

	return sums, nil
}

const sha256HexLen = 64

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}

// errTFNoVersions is a versions document that lists nothing the registry could
// serve; the module or provider does not exist as far as clients can tell.
var errTFNoVersions = errors.New("no usable versions")

var (
	// errTFBadDocument marks a discovery, package or module answer that is
	// well-formed JSON but not something the proxy can serve.
	errTFBadDocument = errors.New("unusable upstream document")
	// errTFBadChecksums marks a SHA256SUMS file that vouches for nothing.
	errTFBadChecksums = errors.New("unusable checksum file")
	// errTFNotArchive marks a body that is not an archive of the expected kind.
	errTFNotArchive = errors.New("not an archive")
)

// tfVersionList is the canonical form a module's version list is cached in:
// the valid versions of the upstream document, ascending, without anything else.
type tfVersionList struct {
	Modules []struct {
		Versions []tfVersion `json:"versions"`
	} `json:"modules"`
}

type tfVersion struct {
	Version string `json:"version"`
}

// canonModuleVersions reads an upstream module version list (either registry's
// shape: modules[].versions[].version) and returns its canonical form.
func canonModuleVersions(r io.Reader) ([]byte, error) {
	var doc tfVersionList
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("module versions document: %w", err)
	}

	var versions []string

	for _, m := range doc.Modules {
		for _, v := range m.Versions {
			if terraform.ValidVersion(v.Version) {
				versions = append(versions, v.Version)
			}
		}
	}

	versions = sortedUnique(versions)
	if len(versions) == 0 {
		return nil, errTFNoVersions
	}

	out := tfVersionList{Modules: make([]struct {
		Versions []tfVersion `json:"versions"`
	}, 1)}
	for _, v := range versions {
		out.Modules[0].Versions = append(out.Modules[0].Versions, tfVersion{Version: v})
	}

	return json.Marshal(out)
}

// parseCanonModuleVersions returns the versions of a cached canonical list.
func parseCanonModuleVersions(data []byte) ([]string, error) {
	var doc tfVersionList
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("cached module versions: %w", err)
	}

	var versions []string

	for _, m := range doc.Modules {
		for _, v := range m.Versions {
			versions = append(versions, v.Version)
		}
	}

	return versions, nil
}

func sortedUnique(versions []string) []string {
	slices.SortFunc(versions, terraform.CompareVersions)

	return slices.Compact(versions)
}

type tfPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type tfProviderVersion struct {
	Version   string       `json:"version"`
	Protocols []string     `json:"protocols"`
	Platforms []tfPlatform `json:"platforms"`
}

// tfProviderVersionList is the provider versions document, in the shape served
// to clients and cached.
type tfProviderVersionList struct {
	Versions []tfProviderVersion `json:"versions"`
	Warnings []string            `json:"warnings"`
}

// canonProviderVersions reads an upstream provider version list and returns the
// document that is cached and served: the versions that are valid and offer at
// least one valid platform, ascending, with protocols and platforms as given.
func canonProviderVersions(r io.Reader) ([]byte, error) {
	var doc tfProviderVersionList
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("provider versions document: %w", err)
	}

	out := tfProviderVersionList{Versions: make([]tfProviderVersion, 0, len(doc.Versions))}

	for _, v := range doc.Versions {
		if !terraform.ValidVersion(v.Version) {
			continue
		}

		pv := tfProviderVersion{Version: v.Version, Protocols: []string{}, Platforms: []tfPlatform{}}

		for _, proto := range v.Protocols {
			if len(proto) <= 16 {
				pv.Protocols = append(pv.Protocols, proto)
			}
		}

		for _, p := range v.Platforms {
			if terraform.ValidPlatformPart(p.OS) && terraform.ValidPlatformPart(p.Arch) {
				pv.Platforms = append(pv.Platforms, p)
			}
		}

		if len(pv.Platforms) > 0 {
			out.Versions = append(out.Versions, pv)
		}
	}

	if len(out.Versions) == 0 {
		return nil, errTFNoVersions
	}

	slices.SortStableFunc(out.Versions, func(a, b tfProviderVersion) int { return terraform.CompareVersions(a.Version, b.Version) })
	out.Versions = slices.CompactFunc(out.Versions, func(a, b tfProviderVersion) bool { return a.Version == b.Version })

	return json.Marshal(out)
}

// tfPackageDoc is a provider package document (the answer of
// .../{version}/download/{os}/{arch}). Signing keys are kept verbatim: the
// proxy serves the upstream's keys, never its own.
type tfPackageDoc struct {
	Protocols           []string        `json:"protocols"`
	OS                  string          `json:"os"`
	Arch                string          `json:"arch"`
	Filename            string          `json:"filename"`
	DownloadURL         string          `json:"download_url"`
	ShasumsURL          string          `json:"shasums_url"`
	ShasumsSignatureURL string          `json:"shasums_signature_url"`
	Shasum              string          `json:"shasum"`
	SigningKeys         json.RawMessage `json:"signing_keys,omitempty"`
}

// parseProviderPackage reads and validates a package document for the platform
// os/arch, resolving relative URLs against docURL (the URL it was fetched
// from). Every URL is absolute http(s) afterwards. The file names that end up
// in cache keys and ticket URLs must be safe, and the zip must be named like one.
func parseProviderPackage(data []byte, docURL *url.URL, osName, arch string) (*tfPackageDoc, error) {
	var doc tfPackageDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("provider package document: %w", err)
	}

	if doc.OS != osName || doc.Arch != arch {
		return nil, fmt.Errorf("%w: provider package document is for %s_%s, want %s_%s", errTFBadDocument, doc.OS, doc.Arch, osName, arch)
	}

	if !validTFFileName(doc.Filename) || !strings.HasSuffix(doc.Filename, ".zip") {
		return nil, fmt.Errorf("%w: provider package file name %q", errTFBadDocument, truncate(doc.Filename, 80))
	}

	if len(doc.Shasum) != sha256HexLen {
		return nil, fmt.Errorf("%w: provider package document has no sha256 shasum", errTFBadDocument)
	}

	if _, err := hex.DecodeString(doc.Shasum); err != nil {
		return nil, fmt.Errorf("%w: provider package shasum is not hex", errTFBadDocument)
	}

	doc.Shasum = strings.ToLower(doc.Shasum)

	var err error

	if doc.DownloadURL, err = absoluteHTTPURL(doc.DownloadURL, docURL); err != nil {
		return nil, fmt.Errorf("download_url: %w", err)
	}

	if doc.ShasumsURL, err = absoluteHTTPURL(doc.ShasumsURL, docURL); err != nil {
		return nil, fmt.Errorf("shasums_url: %w", err)
	}

	if doc.ShasumsSignatureURL, err = absoluteHTTPURL(doc.ShasumsSignatureURL, docURL); err != nil {
		return nil, fmt.Errorf("shasums_signature_url: %w", err)
	}

	// The three files share a cache directory: their names must be usable and
	// tell them apart.
	sums, sig := urlBase(doc.ShasumsURL), urlBase(doc.ShasumsSignatureURL)
	if !validTFFileName(sums) || !validTFFileName(sig) || sums == sig || sums == doc.Filename || sig == doc.Filename {
		return nil, fmt.Errorf("%w: provider package file names are unusable", errTFBadDocument)
	}

	if len(doc.SigningKeys) > 0 {
		var obj map[string]json.RawMessage
		if json.Unmarshal(doc.SigningKeys, &obj) != nil {
			return nil, fmt.Errorf("%w: signing_keys is not an object", errTFBadDocument)
		}
	}

	if doc.Protocols == nil {
		doc.Protocols = []string{}
	}

	return &doc, nil
}

// absoluteHTTPURL resolves ref against base and requires an http(s) URL.
func absoluteHTTPURL(ref string, base *url.URL) (string, error) {
	u, err := url.Parse(ref)
	if err != nil || ref == "" {
		return "", fmt.Errorf("%w: not a URL", errTFBadDocument)
	}

	u = base.ResolveReference(u)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("%w: not an http(s) URL", errTFBadDocument)
	}

	return u.String(), nil
}

// urlBase returns the last path segment of an absolute URL.
func urlBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return u.Path[strings.LastIndexByte(u.Path, '/')+1:]
}

// verifyTFArchive checks the magic bytes of an archive of the given kind
// (extension without dot): a page or an error body is not an archive.
func verifyTFArchive(ext string, spool io.Reader) error {
	var magic [6]byte

	n, err := io.ReadFull(spool, magic[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("read archive header: %w", err)
	}

	head := magic[:n]

	switch ext {
	case "zip":
		if bytes.HasPrefix(head, []byte("PK")) {
			return nil
		}
	case "tar.gz", "tgz":
		if bytes.HasPrefix(head, []byte{0x1f, 0x8b}) {
			return nil
		}
	case "tar.bz2", "tbz2":
		if bytes.HasPrefix(head, []byte("BZh")) {
			return nil
		}
	case "tar.xz", "txz":
		if bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}) {
			return nil
		}
	case "tar":
		// A tar archive has no leading magic (ustar sits at offset 257): all
		// that can be told here is that it is not a markup or JSON answer.
		if n > 0 && head[0] != '<' && head[0] != '{' {
			return nil
		}
	}

	return fmt.Errorf("%w: body is not a %s archive", errTFNotArchive, ext)
}

// archiveContentType is the media type a cached module archive is served as.
func archiveContentType(ext string) string {
	switch ext {
	case "zip":
		return "application/zip"
	case "tar.gz", "tgz":
		return "application/gzip"
	case "tar.bz2", "tbz2":
		return "application/x-bzip2"
	case "tar.xz", "txz":
		return "application/x-xz"
	}

	return "application/x-tar"
}
