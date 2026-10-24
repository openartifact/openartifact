// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// FuzzOCIManifestType feeds arbitrary upstream manifest bodies and Content-Type
// headers to the proxy's media type sniffing: a manifest is accepted only when
// it is a JSON object, and the type it is served and cached under is always a
// registry JSON type, whatever the upstream claimed.
func FuzzOCIManifestType(f *testing.F) {
	f.Add("application/vnd.oci.image.index.v1+json", `{"schemaVersion":2}`)
	f.Add("application/json", `{"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	f.Add("text/html", `{"mediaType":"text/html"}`)
	f.Add("", `{"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json; q=1"}`)
	f.Add("", `{"mediaType":5}`)
	f.Add("", `[1]`)
	f.Add("", `{} trailing`)
	f.Add("", "")

	f.Fuzz(func(t *testing.T, header, body string) {
		mt, err := manifestType(header, strings.NewReader(body))
		if err != nil {
			if !errors.Is(err, errBadManifest) {
				t.Fatalf("manifestType(%q, %q) failed with %v, want errBadManifest", header, body, err)
			}

			return
		}

		if safe, ok := ocipath.SafeMediaType(mt); !ok || safe != mt {
			t.Fatalf("manifestType(%q, %q) = %q, not a bare registry JSON type", header, body, mt)
		}

		if first := strings.TrimLeft(body, " \t\r\n"); !strings.HasPrefix(first, "{") {
			t.Fatalf("manifestType(%q, %q) accepted a body that is not a JSON object", header, body)
		}
	})
}

// FuzzOCIDigesterMatches hashes arbitrary bodies and checks them against
// arbitrary digests, as the proxy does with the digest it asked for and the one
// the upstream announced: a well-formed digest matches exactly the content it
// names, whichever other digest is hashed alongside, and a digest that names
// an algorithm of the registry is never waved through.
func FuzzOCIDigesterMatches(f *testing.F) {
	body := []byte("manifest")
	sum256 := sha256.Sum256(body)
	sum512 := sha512.Sum512(body)

	f.Add(body, "sha256:"+hex.EncodeToString(sum256[:]), "")
	f.Add(body, "sha512:"+hex.EncodeToString(sum512[:]), "sha256:"+hex.EncodeToString(sum256[:]))
	f.Add(body, "sha256:"+strings.Repeat("0", 64), "sha512:"+strings.Repeat("0", 128))
	f.Add(body, "SHA256:"+strings.ToUpper(hex.EncodeToString(sum256[:])), "sha256:")
	f.Add([]byte(nil), "", "md5:abc")

	f.Fuzz(func(t *testing.T, content []byte, first, second string) {
		d := newDigester(first, second)
		_, _ = d.Write(content)

		s256 := sha256.Sum256(content)
		s512 := sha512.Sum512(content)
		want := map[string]string{
			"sha256": "sha256:" + hex.EncodeToString(s256[:]),
			"sha512": "sha512:" + hex.EncodeToString(s512[:]),
		}

		if got := d.sha256Hex(); "sha256:"+got != want["sha256"] {
			t.Fatalf("sha256Hex = %q, want %q", got, want["sha256"])
		}

		for _, digest := range []string{first, second} {
			got := d.matches(digest)

			if digestRE.MatchString(digest) {
				algo, _, _ := strings.Cut(digest, ":")
				if got != (digest == want[algo]) {
					t.Fatalf("matches(%q) = %v for a body hashing to %q", digest, got, want[algo])
				}
			}

			if digest == want["sha256"] || digest == want["sha512"] {
				if !got {
					t.Fatalf("matches(%q) = false for the body's own digest", digest)
				}
			}
		}
	})
}

// FuzzOCIParseChallenge feeds arbitrary WWW-Authenticate values from an
// upstream registry: parsing terminates, the scheme is a single lower-cased
// word, parameter names are lower-case, and the parameters survive being
// rendered as a quoted challenge and parsed again.
func FuzzOCIParseChallenge(f *testing.F) {
	f.Add(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`)
	f.Add(`Basic realm="Registry"`)
	f.Add(`Bearer realm="a\"b,c=d\\",service=x`)
	f.Add(`bearer REALM = "x" , Service=y`)
	f.Add(`Bearer realm="unterminated`)
	f.Add(`Bearer =x,,=,`)
	f.Add("")

	f.Fuzz(func(t *testing.T, header string) {
		scheme, params := parseChallenge(header)

		if strings.Contains(scheme, " ") {
			t.Fatalf("scheme %q of %q holds a space", scheme, header)
		}

		// Only a space ends the scheme (RFC 7235): "Bearer\trealm=x" is one odd scheme.
		if !isASCII(header) || strings.ContainsFunc(scheme, unicode.IsSpace) {
			return
		}

		if scheme != strings.ToLower(scheme) {
			t.Fatalf("scheme %q of %q is not lower-case", scheme, header)
		}

		keys := make([]string, 0, len(params))
		plain := true

		for k := range params {
			if k != strings.ToLower(k) {
				t.Fatalf("parameter name %q of %q is not lower-case", k, header)
			}

			// A name that is not a token (",=" parses to the name ",") is
			// malformed input the parser need not reproduce from its own rendering.
			plain = plain && isParamName(k)

			keys = append(keys, k)
		}

		if !plain {
			return
		}

		slices.Sort(keys)

		var rendered strings.Builder

		rendered.WriteString(scheme)

		for i, k := range keys {
			if i > 0 {
				rendered.WriteString(",")
			}

			escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(params[k])
			fmt.Fprintf(&rendered, ` %s="%s"`, k, escaped)
		}

		scheme2, params2 := parseChallenge(rendered.String())
		if scheme2 != scheme || len(params2) != len(params) {
			t.Fatalf("challenge %q rendered as %q parses to %q %v, want %q %v", header, rendered.String(), scheme2, params2, scheme, params)
		}

		for k, v := range params {
			if params2[k] != v {
				t.Fatalf("challenge %q rendered as %q lost %q: %q, want %q", header, rendered.String(), k, params2[k], v)
			}
		}
	})
}

// isParamName reports whether k is a plain auth-param name: letters, digits,
// "-", "_" and ".".
func isParamName(k string) bool {
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && !strings.ContainsRune("-_.", r) {
			return false
		}
	}

	return k != ""
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}

	return true
}

// FuzzOCIRewriteNextLink feeds arbitrary upstream Link headers of a tag
// listing: the rewritten header always points back into the proxy's own
// listing, with the upstream's query and nothing that could end the header
// value or add one.
func FuzzOCIRewriteNextLink(f *testing.F) {
	f.Add(`</v2/library/alpine/tags/list?last=3.9&n=100>; rel="next"`, "docker/alpine")
	f.Add(`<https://evil.example/steal?x=1>; rel=next`, "repo/app")
	f.Add("<http://a/?x=1\r\nSet-Cookie: a=b>; rel=\"next\"", "r/i")
	f.Add(`<?>; rel="next"`, "r/i")
	f.Add(`<http://a/b?q=>>; rel="next"`, "r/i")
	f.Add(`</v2/x/tags/list?last=a#frag>;rel="next"`, "r/i")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, link, name string) {
		out := rewriteNextLink(link, name)
		if out == "" {
			return
		}

		const (
			suffix = `>; rel="next"`
			middle = "/tags/list?"
		)

		prefix := "</v2/" + name + middle
		if !strings.HasPrefix(out, prefix) || !strings.HasSuffix(out, suffix) || len(out) < len(prefix)+len(suffix) {
			t.Fatalf("rewriteNextLink(%q, %q) = %q does not point at the listing", link, name, out)
		}

		query := out[len(prefix) : len(out)-len(suffix)]
		if query == "" || strings.ContainsAny(query, ">#") {
			t.Fatalf("rewriteNextLink(%q, %q) = %q has query %q", link, name, out, query)
		}

		for i := range len(query) {
			if query[i] < 0x20 || query[i] == 0x7f {
				t.Fatalf("rewriteNextLink(%q, %q) = %q carries a control character", link, name, out)
			}
		}
	})
}

// FuzzOCIRealmMayReceiveCredentials feeds arbitrary registry and token realm
// URLs: the upstream credentials go only to the registry's own origin or to an
// https endpoint in the same domain, never to a different host.
func FuzzOCIRealmMayReceiveCredentials(f *testing.F) {
	f.Add("https://registry-1.docker.io", "https://auth.docker.io/token")
	f.Add("https://registry.example.com", "https://registry.example.com:443/token")
	f.Add("https://registry.example.com", "https://registry.example.com@evil.test/token")
	f.Add("https://registry.example.com", "http://auth.example.com/token")
	f.Add("http://10.0.0.1:5000", "http://10.0.0.1:5000/token")
	f.Add("https://a.github.io", "https://b.github.io/token")
	f.Add("https://localhost", "https://localhost./token")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, registry, realm string) {
		realmURL, err := url.Parse(realm)
		if err != nil {
			return
		}

		baseURL, err := url.Parse(registry)
		if err != nil {
			return
		}

		if !realmMayReceiveCredentials(ociUpstream{base: registry}, realmURL) {
			return
		}

		ro, bo := originOf(realmURL), originOf(baseURL)
		if ro == bo {
			return
		}

		if ro.scheme != "https" {
			t.Fatalf("credentials for %q go to the %s realm %q", registry, ro.scheme, realm)
		}

		if ro.host == "" || bo.host == "" || lastLabel(ro.host) != lastLabel(bo.host) {
			t.Fatalf("credentials for %q go to the unrelated realm %q", registry, realm)
		}
	})
}

func lastLabel(host string) string { return host[strings.LastIndex(host, ".")+1:] }

// FuzzOCITarget feeds arbitrary upstream URLs and image names to the mapping of
// a proxied image to its upstream: the registry is scheme and host only, the
// image keeps its name (Docker Hub official images gain library/), and the
// registry's own origin may always receive the stored credentials.
func FuzzOCITarget(f *testing.F) {
	f.Add("https://registry-1.docker.io", "alpine")
	f.Add("https://index.docker.io/v1/", "org/app")
	f.Add("https://ghcr.io/ignored/path?x=1", "owner/app")
	f.Add("http://localhost:5000", "app")
	f.Add("//host/x", "app")
	f.Add("https://", "app")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, upstream, image string) {
		up, err := ociTarget(&models.Repository{UpstreamURL: upstream}, image)
		if err != nil {
			return
		}

		u, perr := url.Parse(upstream)
		if perr != nil || u.Host == "" {
			t.Fatalf("ociTarget(%q) succeeded for an unusable URL: %v", upstream, perr)
		}

		if up.base != u.Scheme+"://"+u.Host || strings.ContainsAny(up.base[len(u.Scheme)+3:], "/?#") {
			t.Fatalf("ociTarget(%q).base = %q, want scheme and host only", upstream, up.base)
		}

		if up.name != image && up.name != "library/"+image {
			t.Fatalf("ociTarget(%q, %q).name = %q", upstream, image, up.name)
		}

		if up.name != image && strings.Contains(image, "/") {
			t.Fatalf("ociTarget(%q, %q) prefixed an image that has a namespace: %q", upstream, image, up.name)
		}

		if u.Scheme != "http" && u.Scheme != "https" {
			return
		}

		baseURL, perr := url.Parse(up.base)
		if perr != nil {
			return
		}

		if !realmMayReceiveCredentials(up, baseURL) {
			t.Fatalf("the registry %q may not receive credentials at its own origin", up.base)
		}
	})
}

// FuzzOCIRewriteTagList feeds arbitrary upstream tag listings to the rewrite
// of their repository name: a listing is accepted only when it is a JSON
// object, and the result names the repository the client used and keeps every
// other member of the upstream's document.
func FuzzOCIRewriteTagList(f *testing.F) {
	f.Add(`{"name":"library/alpine","tags":["3.19","latest"]}`, "alpine")
	f.Add(`{"tags":null,"extra":{"a":[1,2.5e3,"<&>"]}}`, "my/img")
	f.Add(`{"name":5}`, "a/b")
	f.Add(`{"name":"a","name":"b","tags":[]}`, "x")
	f.Add(`[]`, "x")
	f.Add(`{`, "x")
	f.Add(``, "x")

	f.Fuzz(func(t *testing.T, body, name string) {
		if !utf8.ValidString(name) {
			return // a validated image name
		}

		out, err := rewriteTagList([]byte(body), name)

		var in map[string]any

		isObject := json.Unmarshal([]byte(body), &in) == nil && in != nil
		if (err == nil) != isObject {
			t.Fatalf("rewriteTagList(%q) = %q, %v but the body is a JSON object = %v", body, out, err, isObject)
		}

		if err != nil {
			return
		}

		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil || got == nil {
			t.Fatalf("rewriteTagList(%q) = %q is not a JSON object: %v", body, out, err)
		}

		if got["name"] != name {
			t.Fatalf("rewriteTagList(%q, %q) names the repository %v", body, name, got["name"])
		}

		in["name"] = name

		if !reflect.DeepEqual(in, got) {
			t.Fatalf("rewriteTagList(%q, %q) = %q: the other members differ", body, name, out)
		}
	})
}
