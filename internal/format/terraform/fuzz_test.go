// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
)

// seedModuleZip and friends are the valid inputs the targets start from.
func seedZip(entries ...[2]string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, _ := zw.Create(e[0])
		_, _ = w.Write([]byte(e[1]))
	}

	_ = zw.Close()

	return buf.Bytes()
}

// FuzzModuleArchive feeds arbitrary bytes to the module archive validator:
// it must never panic, must refuse with an archiveError, and what it accepts
// must be an archive of the kind its magic bytes say.
func FuzzModuleArchive(f *testing.F) {
	f.Add(seedZip([2]string{"main.tf", `variable "x" {}`}))
	f.Add(seedZip([2]string{"repo-1.0/main.tf", ""}, [2]string{"repo-1.0/README.md", "x"}))
	f.Add(seedZip([2]string{"../evil.tf", "x"}))
	f.Add(seedZip([2]string{"main.tf", "a"}, [2]string{"main.tf", "b"}))
	f.Add([]byte("PK\x03\x04"))
	f.Add([]byte("\x1f\x8b\x08\x00\x00\x00\x00\x00"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := validateModuleArchive(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			if _, ok := errors.AsType[*archiveError](err); !ok {
				t.Fatalf("error %T (%v) is not an *archiveError", err, err)
			}

			return
		}

		if info.ext != detectArchive(data) || (info.ext != extZip && info.ext != extTarGz) {
			t.Fatalf("accepted as %q, magic says %q", info.ext, detectArchive(data))
		}
	})
}

// FuzzProviderArchive does the same for provider zips, with the coordinates
// the binary name is checked against fuzzed too.
func FuzzProviderArchive(f *testing.F) {
	f.Add(seedZip([2]string{"terraform-provider-aws_v1.2.3", "bin"}), "aws", "1.2.3")
	f.Add(
		seedZip(
			[2]string{"terraform-provider-aws_v1.2.3_x5.exe", "bin"},
			[2]string{manifestName, `{"metadata":{"protocol_versions":["5.0","6.0"]}}`},
		),
		"aws",
		"1.2.3",
	)
	f.Add(seedZip([2]string{manifestName, `{`}), "aws", "1.2.3")
	f.Add(seedZip([2]string{"terraform-provider-aws_v1.2.3", "bin"}), "", "")
	f.Add([]byte("PK\x03\x04"), "x", "1.0.0")

	f.Fuzz(func(t *testing.T, data []byte, typ, version string) {
		info, err := validateProviderArchive(bytes.NewReader(data), int64(len(data)), typ, version)
		if err != nil {
			if _, ok := errors.AsType[*archiveError](err); !ok {
				t.Fatalf("error %T (%v) is not an *archiveError", err, err)
			}

			return
		}

		if info.ext != extZip || detectArchive(data) != extZip {
			t.Fatalf("accepted a non-zip as a provider: %q", info.ext)
		}

		if len(info.protocols) > maxProtocols || !slices.IsSorted(info.protocols) {
			t.Fatalf("protocols = %v", info.protocols)
		}

		for _, p := range info.protocols {
			if !validProtocol(p) {
				t.Fatalf("invalid protocol %q accepted", p)
			}
		}
	})
}

// FuzzEntryName: a name cleanEntryName accepts is a local, normalised path,
// and cleaning it again changes nothing.
func FuzzEntryName(f *testing.F) {
	for _, s := range []string{"main.tf", "./main.tf", "a/b/", "../x", "a/../../x", "/abs", `a\b`, "c:x", "a\x00b", "", ".", "..", "a//b"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		clean, err := cleanEntryName(name)
		if err != nil {
			return
		}

		if clean == "" {
			return
		}

		if !filepath.IsLocal(clean) || strings.ContainsAny(clean, "\\\x00") || strings.HasSuffix(clean, "/") || path.Clean(clean) != clean {
			t.Fatalf("cleanEntryName(%q) = %q is not a clean local path", name, clean)
		}

		again, err := cleanEntryName(clean)
		if err != nil || again != clean {
			t.Fatalf("cleaning %q again gave %q, %v", clean, again, err)
		}
	})
}

// FuzzTicket: a string is only ever accepted when it carries a valid
// signature, and an accepted ticket re-encodes to a ticket that decodes to the
// same thing.
func FuzzTicket(f *testing.F) {
	key := deriveTicketKey([]byte(testSecret))
	far := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

	for _, tk := range []ticket{
		{repoID: "r", kind: kindModule, a: "vpc", b: "aws", c: "1.0.0", expires: far},
		{repoID: "r", kind: kindProviderZip, a: "aws", b: "1.0.0", c: "linux_amd64", expires: far},
		{repoID: "r", kind: kindProviderSums, a: "aws", b: "1.0.0", expires: far},
	} {
		f.Add(encodeTicket(key, tk))
	}

	f.Add("")
	f.Add(".")
	f.Add("YQ.YQ")
	f.Add(strings.Repeat("A", 200) + "." + strings.Repeat("B", 43))

	f.Fuzz(func(t *testing.T, s string) {
		got, err := decodeTicket(key, s, now)
		if err != nil {
			return
		}

		if !got.valid() || !got.expires.After(now) {
			t.Fatalf("accepted an invalid or expired ticket: %+v", got)
		}

		again, err := decodeTicket(key, encodeTicket(key, got), now)
		if err != nil || again.repoID != got.repoID || again.kind != got.kind || again.a != got.a || again.b != got.b || again.c != got.c {
			t.Fatalf("round trip changed the ticket: %+v -> %+v (%v)", got, again, err)
		}

		if _, err := decodeTicket(deriveTicketKey([]byte("another secret, another secret!!")), s, now); err == nil {
			t.Fatal("a ticket verified under a different key")
		}
	})
}

// FuzzNames: whatever the validators accept is safe to put in a URL path
// segment and a file name, and versions order consistently.
func FuzzNames(f *testing.F) {
	for _, s := range []string{"vpc", "1.2.3", "1.2.3-rc.1", "v1.0.0", "1.0.0+b", "linux", "a b", "..", "", "-a", "a-", "01.0.0"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		const unsafe = "/\\.\x00 ?#%:|"

		if validName(s) && (strings.ContainsAny(s, unsafe) || len(s) > maxNameLen) {
			t.Fatalf("validName accepted %q", s)
		}

		if validPlatformPart(s) && (strings.ContainsAny(s, unsafe+"_-") || len(s) > maxPlatformLen) {
			t.Fatalf("validPlatformPart accepted %q", s)
		}

		if !validVersion(s) {
			return
		}

		if strings.ContainsAny(s, "/\\\x00 ?#%:|+") || len(s) > maxVersionLen || s[0] == 'v' {
			t.Fatalf("validVersion accepted %q", s)
		}

		if compareVersions(s, s) != 0 || latestVersion([]string{s}) != s {
			t.Fatalf("version %q does not order against itself", s)
		}
	})
}

// FuzzSHA256SUMS: the file has one line per entry, sorted by file name, and
// does not depend on the order of the input.
func FuzzSHA256SUMS(f *testing.F) {
	f.Add("aa", "terraform-provider-x_1.0.0_linux_amd64.zip", "bb", "terraform-provider-x_1.0.0_darwin_arm64.zip")
	f.Add("", "", "", "")
	f.Add("a b", "n\nm", "c", "n\nm")

	f.Fuzz(func(t *testing.T, sha1, name1, sha2, name2 string) {
		a, b := sumEntry{sha1, name1}, sumEntry{sha2, name2}

		forward := buildSHA256SUMS([]sumEntry{a, b})
		backward := buildSHA256SUMS([]sumEntry{b, a})

		if name1 != name2 && !bytes.Equal(forward, backward) {
			t.Fatalf("order of the input changed the output:\n%q\n%q", forward, backward)
		}

		if !bytes.HasSuffix(forward, []byte("\n")) {
			t.Fatalf("output is not newline terminated: %q", forward)
		}

		clean := func(s string) bool { return s != "" && !strings.ContainsAny(s, " \t\r\n") }
		if clean(sha1) && clean(name1) && clean(sha2) && clean(name2) {
			lines := strings.Split(strings.TrimSuffix(string(forward), "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("lines = %q", lines)
			}

			var names []string

			for _, l := range lines {
				sha, name, ok := strings.Cut(l, "  ")
				if !ok || !clean(sha) || !clean(name) {
					t.Fatalf("line %q is not '<sha>  <name>'", l)
				}

				names = append(names, name)
			}

			if !slices.IsSorted(names) {
				t.Fatalf("names not sorted: %v", names)
			}
		}
	})
}

// FuzzProtocols: accepted protocol lists are sorted, unique and well formed.
func FuzzProtocols(f *testing.F) {
	for _, s := range []string{"5.0", "5.0,6.0", "6.0,5.0,5.0", "5", "a.b", "", ",", "1.0,2.0,3.0,4.0,5.0,6.0,7.0,8.0,9.0", "999.999"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := normalizeProtocols(strings.Split(s, ","))
		if err != nil {
			return
		}

		if len(got) == 0 || len(got) > maxProtocols || !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
			t.Fatalf("normalizeProtocols(%q) = %v", s, got)
		}

		for _, p := range got {
			if !validProtocol(p) {
				t.Fatalf("invalid protocol %q accepted", p)
			}
		}
	})
}

// FuzzUploadModule uploads an arbitrary body under arbitrary coordinates and,
// when it is accepted, follows what a Terraform client does next: list the
// versions, ask for the download and fetch the archive from the ticket URL. A
// rejected upload must leave nothing behind and never be a server error.
func FuzzUploadModule(f *testing.F) {
	f.Add(seedZip([2]string{"main.tf", "x"}), "vpc", "aws", "1.0.0")
	f.Add(seedZip([2]string{"main.tf", "x"}), "vpc", "aws", "1.0.0-rc.1")
	f.Add(seedZip([2]string{"../main.tf", "x"}), "vpc", "aws", "1.0.0")
	f.Add(seedZip([2]string{"main.tf", "x"}), "a.b", "aws", "v1.0.0")
	f.Add([]byte("junk"), "vpc", "versions", "1.0.0")

	f.Fuzz(func(t *testing.T, body []byte, name, system, version string) {
		e := newEnv(t)

		if !pathSafe(name, system, version) {
			return
		}

		w := e.do(http.MethodPut, "/repos/myrepo/_api/modules/"+name+"/"+system+"/"+version, bytes.NewReader(body))
		if w.Code != http.StatusCreated {
			if w.Code >= http.StatusInternalServerError || len(e.db.mods) != 0 || len(e.bs.Blobs) != 0 || e.db.used != 0 {
				t.Fatalf(
					"rejected upload answered %d and left rows=%d blobs=%d used=%d",
					w.Code,
					len(e.db.mods),
					len(e.bs.Blobs),
					e.db.used,
				)
			}

			return
		}

		if !validUploadName(name) || !validModuleSystem(system) || !validVersion(version) {
			t.Fatalf("accepted the coordinates %q %q %q", name, system, version)
		}

		mint := e.do(http.MethodGet, "/repos/myrepo/"+name+"/"+system+"/"+version+"/download", nil)
		if mint.Code != http.StatusNoContent {
			t.Fatalf("download call = %d", mint.Code)
		}

		got := httptest.NewRecorder()
		e.router().ServeHTTP(got, formattest.Request(http.MethodGet, ticketPathOf(t, mint.Header().Get("X-Terraform-Get")), nil))

		if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), body) {
			t.Fatalf("ticket download = %d, %d bytes, uploaded %d", got.Code, got.Body.Len(), len(body))
		}

		if v := e.do(
			http.MethodGet,
			"/repos/myrepo/"+name+"/"+system+"/versions",
			nil,
		); v.Code != http.StatusOK ||
			!strings.Contains(v.Body.String(), `"`+version+`"`) {
			t.Fatalf("versions = %d %s", v.Code, v.Body)
		}
	})
}

// FuzzUploadProvider is FuzzUploadModule for providers: what is accepted
// has to be describable, its checksum file has to list the zip with the
// digest of the uploaded bytes and the signature has to be there.
func FuzzUploadProvider(f *testing.F) {
	signer := signerResolver{newSigner(f)}

	f.Add(seedZip([2]string{"terraform-provider-aws_v1.2.3", "bin"}), "aws", "1.2.3", "linux", "amd64", "")
	f.Add(seedZip([2]string{"terraform-provider-aws_v1.2.3", "bin"}), "aws", "1.2.3", "linux", "amd64", "5.0,6.0")
	f.Add(seedZip([2]string{"terraform-provider-aws_v1.2.3", "bin"}), "aws", "1.2.3", "Linux", "amd64", "x")
	f.Add([]byte("junk"), "aws", "1.2.3", "linux", "amd64", "")

	f.Fuzz(func(t *testing.T, body []byte, typ, version, osName, arch, protocols string) {
		e := newEnv(t)
		e.h.WithSigner(signer)

		if !pathSafe(typ, version, osName, arch, protocols) {
			return
		}

		target := "/repos/myrepo/_api/providers/" + typ + "/" + version + "/" + osName + "/" + arch
		if protocols != "" {
			target += "?protocols=" + protocols
		}

		w := e.do(http.MethodPut, target, bytes.NewReader(body))
		if w.Code != http.StatusCreated {
			if w.Code >= http.StatusInternalServerError || len(e.db.provs) != 0 || len(e.bs.Blobs) != 0 || e.db.used != 0 {
				t.Fatalf(
					"rejected upload answered %d and left rows=%d blobs=%d used=%d",
					w.Code,
					len(e.db.provs),
					len(e.bs.Blobs),
					e.db.used,
				)
			}

			return
		}

		pkg := e.do(http.MethodGet, "/repos/myrepo/"+typ+"/"+version+"/download/"+osName+"/"+arch, nil)
		if pkg.Code != http.StatusOK || !strings.Contains(pkg.Body.String(), `"shasum":"`+hexSHA(body)+`"`) {
			t.Fatalf("package description = %d %s", pkg.Code, pkg.Body)
		}

		sums := e.do(http.MethodGet, ticketPathOf(t, jsonField(t, pkg, "shasums_url")), nil)
		if sums.Code != http.StatusOK ||
			!strings.Contains(sums.Body.String(), hexSHA(body)+"  "+providerZipName(typ, version, osName, arch)+"\n") {
			t.Fatalf("checksums = %d %q", sums.Code, sums.Body)
		}

		sig := e.do(http.MethodGet, ticketPathOf(t, jsonField(t, pkg, "shasums_signature_url")), nil)
		if sig.Code != http.StatusOK || sig.Body.Len() == 0 {
			t.Fatalf("signature = %d, %d bytes", sig.Code, sig.Body.Len())
		}
	})
}

func jsonField(t *testing.T, w *httptest.ResponseRecorder, field string) string {
	t.Helper()

	v, _ := jsonOf[map[string]any](t, w)[field].(string)
	if v == "" {
		t.Fatalf("response has no %q: %s", field, w.Body)
	}

	return v
}

// pathSafe reports whether every part can be sent in a request line as is:
// printable ASCII without the characters that delimit a URL. Anything else is
// the HTTP client's business, not the handler's.
func pathSafe(parts ...string) bool {
	for _, p := range parts {
		for _, r := range p {
			if r <= ' ' || r >= 0x7f || strings.ContainsRune("/?#%&=;\\", r) {
				return false
			}
		}
	}

	return true
}
