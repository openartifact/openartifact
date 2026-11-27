// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cocoapods

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // a podspec's "sha1" is a checksum by protocol; a corruption check, not authentication
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/openartifact/openartifact/internal/lazyre"
)

// ErrChecksum is a source archive whose bytes do not match the checksum its
// podspec publishes.
var ErrChecksum = errors.New("source archive does not match its checksum")

const (
	// MaxPodspec bounds a podspec JSON document.
	MaxPodspec = 16 << 20

	githubHost       = "github.com"
	githubCodeload   = "https://codeload.github.com/"
	maxArchiveName   = 120
	fallbackFileName = "source"
)

var (
	githubNameRe = lazyre.New(`^[A-Za-z0-9_.-]+$`)
	commitRe     = lazyre.New(`^[0-9a-f]{7,40}$`)
	sha256Re     = lazyre.New(`^[0-9a-f]{64}$`)
	sha1Re       = lazyre.New(`^[0-9a-f]{40}$`)
	unsafeName   = lazyre.New(`[^A-Za-z0-9._+-]+`)
)

// Archive is the source archive of one pod version as the proxy serves it: where
// the upstream has it and what vouches for it.
type Archive struct {
	// URL is the absolute http(s) URL the archive is downloaded from.
	URL string
	// File is the file name the archive is served under; it keeps the extension
	// CocoaPods infers the archive type from.
	File string
	// SHA256 and SHA1 are the checksums the podspec publishes, lower case, "" when absent.
	SHA256 string
	SHA1   string
	// Pinned says the archive is fixed by what the podspec names: a checksum or
	// a commit. Anything else (an http archive without checksum, a git tag, which can be moved)
	// may change and is cached for the repository TTL only.
	Pinned bool
}

// Ref is the reference in the archive's download path: a digest of where the
// archive comes from and what it must match, so the path changes whenever the
// podspec's source does and never serves one source's bytes under another's name.
func (a Archive) Ref() string {
	sum := sha256.New()

	for _, s := range []string{a.URL, a.SHA256, a.SHA1, a.File} {
		sum.Write([]byte(s))
		sum.Write([]byte{0})
	}

	return hex.EncodeToString(sum.Sum(nil)[:RefLen/2])
}

// Verify reads r in full and returns ErrChecksum when it does not match the
// checksums of the podspec. An archive with none passes.
func (a Archive) Verify(r io.Reader) error {
	if a.SHA256 == "" && a.SHA1 == "" {
		return nil
	}

	var (
		hashes  []hash.Hash
		writers []io.Writer
		wants   []string
	)

	add := func(h hash.Hash, want string) {
		if want != "" {
			hashes, writers, wants = append(hashes, h), append(writers, h), append(wants, want)
		}
	}

	add(sha256.New(), a.SHA256)
	add(sha1.New(), a.SHA1) //nolint:gosec // see the import

	if _, err := io.Copy(io.MultiWriter(writers...), r); err != nil {
		return fmt.Errorf("read source archive: %w", err)
	}

	for i, h := range hashes {
		if got := hex.EncodeToString(h.Sum(nil)); got != wants[i] {
			return fmt.Errorf("%w: got %s, the podspec says %s", ErrChecksum, got, wants[i])
		}
	}

	return nil
}

// span is the extent of a JSON value inside its document.
type span struct{ start, end int }

// objectSpans returns the extent of the value of every member of the JSON object
// doc (the last one of a repeated key, as Ruby's JSON.parse keeps it). A document
// that is not exactly one object is an error.
func objectSpans(doc []byte) (map[string]span, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%w: not a JSON object", ErrDocument)
	}

	spans := map[string]span{}

	for dec.More() {
		tok, err := dec.Token()

		key, isString := tok.(string)
		if err != nil || !isString {
			return nil, fmt.Errorf("%w: bad object key", ErrDocument)
		}

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDocument, err)
		}

		end := int(dec.InputOffset())
		start := end - len(raw)

		if start < 0 || !bytes.Equal(doc[start:end], raw) {
			return nil, fmt.Errorf("%w: cannot locate the value of %q", ErrDocument, key)
		}

		spans[key] = span{start, end}
	}

	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, fmt.Errorf("%w: unterminated JSON object", ErrDocument)
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after the JSON object", ErrDocument)
	}

	return spans, nil
}

// stringAt decodes the JSON string at sp of doc; ok is false for any other value.
func stringAt(doc []byte, sp span) (string, bool) {
	var s string
	if json.Unmarshal(doc[sp.start:sp.end], &s) != nil {
		return "", false
	}

	return s, true
}

// source is the parsed "source" of a podspec and how to rewrite it.
type source struct {
	archive Archive
	// replace is the extent of the text the rewrite replaces and what it is
	// replaced with, as a function of the proxy URL of the archive.
	replace span
	render  func(proxyURL string) []byte
}

// Podspec is what the proxy reads of a podspec document.
type Podspec struct {
	// Name and Version are the "name" and "version" of the document.
	Name    string
	Version string

	doc []byte
	src *source
}

// ParsePodspec reads a podspec JSON document of the pod version name/version: the
// document must be a JSON object that names that pod and version. A source the
// proxy does not serve is not an error: Archive reports false for it.
func ParsePodspec(doc []byte, name, version string) (*Podspec, error) {
	if len(doc) > MaxPodspec {
		return nil, fmt.Errorf("%w: podspec is larger than %d bytes", ErrDocument, MaxPodspec)
	}

	top, err := objectSpans(doc)
	if err != nil {
		return nil, err
	}

	p := &Podspec{doc: doc}

	for _, f := range []struct {
		key  string
		dst  *string
		want string
	}{{"name", &p.Name, name}, {"version", &p.Version, version}} {
		sp, ok := top[f.key]
		if !ok {
			return nil, fmt.Errorf("%w: the podspec has no %q", ErrDocument, f.key)
		}

		if *f.dst, ok = stringAt(doc, sp); !ok || *f.dst != f.want {
			return nil, fmt.Errorf("%w: the podspec is not of %s %s", ErrDocument, name, version)
		}
	}

	if sp, ok := top["source"]; ok {
		p.src = parseSource(doc, sp)
	}

	return p, nil
}

// Archive is the source archive of the pod version the proxy serves; false when
// the source is one it leaves to the client (see parseSource).
func (p *Podspec) Archive() (Archive, bool) {
	if p.src == nil {
		return Archive{}, false
	}

	return p.src.archive, true
}

// Rewrite returns the podspec with its source pointed at the proxy: archiveURL
// is the proxy URL of the archive. The document is otherwise byte for byte the
// upstream's. ok is false when the source is left alone (the document comes back
// as it is).
func (p *Podspec) Rewrite(archiveURL string) ([]byte, bool) {
	if p.src == nil {
		return p.doc, false
	}

	repl := p.src.render(archiveURL)
	out := make([]byte, 0, len(p.doc)-(p.src.replace.end-p.src.replace.start)+len(repl))
	out = append(out, p.doc[:p.src.replace.start]...)
	out = append(out, repl...)
	out = append(out, p.doc[p.src.replace.end:]...)

	return out, true
}

// parseSource reads the "source" of a podspec at sp. The proxy serves two kinds:
//
//   - an http source ("http": URL, with an optional sha256 or sha1) of any
//     http(s) host, as long as no "headers" have to be sent with the request;
//   - a git source of a GitHub repository at one "tag" or one "commit", which
//     becomes the tarball GitHub serves for that ref. A branch moves, submodules
//     are not part of a GitHub archive, and any other host is the client's to
//     reach, so those stay git sources.
//
// svn and hg sources, and a source that cannot be read, are left alone (nil).
func parseSource(doc []byte, sp span) *source {
	fields, err := objectSpans(doc[sp.start:sp.end])
	if err != nil {
		return nil
	}

	str := func(key string) (string, bool) {
		f, ok := fields[key]
		if !ok {
			return "", false
		}

		return stringAt(doc[sp.start:sp.end], f)
	}

	rawOf := func(key string) ([]byte, bool) {
		f, ok := fields[key]
		if !ok {
			return nil, false
		}

		return bytes.TrimSpace(doc[sp.start+f.start : sp.start+f.end]), true
	}

	_, hasHTTP := fields["http"]
	_, hasGit := fields["git"]

	switch {
	case hasHTTP && !hasGit:
		return parseHTTPSource(sp, fields, str)
	case hasGit && !hasHTTP:
		return parseGitSource(sp, fields, str, rawOf)
	}

	return nil
}

func parseHTTPSource(sp span, fields map[string]span, str func(string) (string, bool)) *source {
	if _, headers := fields["headers"]; headers {
		return nil
	}

	raw, ok := str("http")
	if !ok || !httpURL(raw) {
		return nil
	}

	a := Archive{URL: raw, File: httpFileName(raw)}

	for _, c := range []struct {
		key string
		dst *string
		re  func() *regexp.Regexp
	}{{"sha256", &a.SHA256, sha256Re}, {"sha1", &a.SHA1, sha1Re}} {
		if _, present := fields[c.key]; !present {
			continue
		}

		v, isString := str(c.key)
		if v = strings.ToLower(v); !isString || !c.re().MatchString(v) {
			// A checksum the proxy cannot check is one it cannot vouch with: the client
			// downloads this source itself and reports it.
			return nil
		}

		*c.dst = v
	}

	a.Pinned = a.SHA256 != "" || a.SHA1 != ""

	at := fields["http"]

	return &source{
		archive: a,
		replace: span{sp.start + at.start, sp.start + at.end},
		render:  jsonString,
	}
}

func parseGitSource(sp span, fields map[string]span, str func(string) (string, bool), rawOf func(string) ([]byte, bool)) *source {
	if _, branch := fields["branch"]; branch {
		return nil
	}

	if raw, ok := rawOf("submodules"); ok && string(raw) != "false" {
		return nil
	}

	repoURL, ok := str("git")
	if !ok {
		return nil
	}

	owner, repo, ok := githubRepo(repoURL)
	if !ok {
		return nil
	}

	tag, hasTag := str("tag")
	commit, hasCommit := str("commit")

	_, tagField := fields["tag"]
	_, commitField := fields["commit"]

	var (
		ref    string
		pinned bool
	)

	switch {
	case tagField == commitField: // both or neither: CocoaPods refuses it
		return nil
	case tagField:
		segs, ok := tagSegments(tag)
		if !hasTag || !ok {
			return nil
		}

		ref, pinned = "refs/tags/"+strings.Join(segs, "/"), false
	default:
		if commit = strings.ToLower(commit); !hasCommit || !commitRe().MatchString(commit) {
			return nil
		}

		ref, pinned = commit, true
	}

	name := repo + "-" + strings.ReplaceAll(strings.TrimPrefix(ref, "refs/tags/"), "/", "-") + ".tar.gz"

	return &source{
		archive: Archive{
			URL:    githubCodeload + owner + "/" + repo + "/tar.gz/" + ref,
			File:   safeFileName(name),
			Pinned: pinned,
		},
		replace: sp,
		render: func(proxyURL string) []byte {
			return append(append([]byte(`{"http":`), jsonString(proxyURL)...), `,"type":"tgz"}`...)
		},
	}
}

// tagSegments escapes the segments of a git tag for the path of a codeload URL;
// false for a tag that is not a plain ref name.
func tagSegments(tag string) ([]string, bool) {
	if tag == "" || len(tag) > MaxSegmentLen {
		return nil, false
	}

	parts := strings.Split(tag, "/")
	for i, s := range parts {
		if !validSegment(s) || strings.ContainsAny(s, "~^:?*[\\") {
			return nil, false
		}

		parts[i] = url.PathEscape(s)
	}

	return parts, true
}

// githubRepo is the owner and name of the GitHub repository a podspec's git URL
// names: https://github.com/OWNER/REPO(.git), also with the http or git scheme.
func githubRepo(raw string) (string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}

	if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "git" {
		return "", "", false
	}

	if h := strings.ToLower(u.Hostname()); h != githubHost && h != "www."+githubHost || u.Port() != "" {
		return "", "", false
	}

	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")

	owner, repo, ok := strings.Cut(path, "/")
	if !ok || owner == "." || owner == ".." || repo == "." || repo == ".." || strings.Contains(repo, "/") {
		return "", "", false
	}

	if !githubNameRe().MatchString(owner) || !githubNameRe().MatchString(repo) {
		return "", "", false
	}

	return owner, repo, true
}

// httpURL reports whether s is an absolute http(s) URL with a host.
func httpURL(s string) bool {
	u, err := url.Parse(s)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// httpFileName is the file name an http source is served under: the last path
// segment of its URL.
func httpFileName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return fallbackFileName
	}

	name := u.Path[strings.LastIndexByte(u.Path, '/')+1:]

	return safeFileName(name)
}

// safeFileName reduces name to characters that need no escaping and are not
// path syntax, keeping its end (the extension) when it has to be shortened.
func safeFileName(name string) string {
	name = unsafeName().ReplaceAllString(name, "_")
	if len(name) > maxArchiveName {
		name = name[len(name)-maxArchiveName:]
	}

	if strings.Trim(name, ".") == "" {
		return fallbackFileName
	}

	return name
}

// jsonString encodes s as a JSON string without escaping HTML characters.
func jsonString(s string) []byte {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(s); err != nil {
		// A Go string always encodes.
		panic(fmt.Sprintf("cocoapods: encode string: %v", err))
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
}
