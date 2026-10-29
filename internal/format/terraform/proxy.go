// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"net/http"
	"net/url"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
)

// A proxy repository of this format answers the registry protocols from
// internal/proxy and hands out the same ticketed download URLs as a hosted one.
// The tickets (and the credential-free route that redeems them) are shared: a
// ticket names a repository, and the route serves a hosted repository from its
// tables and a proxy repository through ProxyServer.

// DownloadKind names which file of a proxy repository a ticket entitles its
// bearer to download.
type DownloadKind string

const (
	// DownloadModule is a module archive: A is the module name, B its system, C the version.
	DownloadModule DownloadKind = DownloadKind(kindModule)
	// DownloadProviderZip is a provider platform zip: A is the type, B the
	// version, C the platform ("os_arch").
	DownloadProviderZip DownloadKind = DownloadKind(kindProviderZip)
	// DownloadProviderSums is the SHA256SUMS file of a provider version; C is
	// the platform whose package document names it.
	DownloadProviderSums DownloadKind = DownloadKind(kindProviderSums)
	// DownloadProviderSumsSig is the detached signature of that file.
	DownloadProviderSumsSig DownloadKind = DownloadKind(kindProviderSumSig)
)

// Download is a verified ticket as a proxy repository serves it. File is the
// file name the request asked for; the server serves it only when it is the one
// it would have minted for the coordinates.
type Download struct {
	Kind    DownloadKind
	A, B, C string
	File    string
}

// ProxyServer serves the downloads of proxy repositories (internal/proxy).
type ProxyServer interface {
	ServeTerraformDownload(w http.ResponseWriter, r *http.Request, repo *models.Repository, d Download)
}

// WithProxy sets the service that redeems the tickets of proxy repositories.
// Without it such a ticket answers 404.
func (h *HTTPHandler) WithProxy(p ProxyServer) *HTTPHandler {
	h.proxy = p

	return h
}

// serveProxyTicket redeems a ticket minted for a proxy repository.
func (h *HTTPHandler) serveProxyTicket(w http.ResponseWriter, r *http.Request, repo *models.Repository, t ticket, file string) {
	if h.proxy == nil || repo.RepoType != repotype.Proxy {
		writeErrors(w, http.StatusNotFound, "not found")
		return
	}

	h.proxy.ServeTerraformDownload(w, r, repo, Download{Kind: DownloadKind(t.kind), A: t.a, B: t.b, C: t.c, File: file})
}

// Minter mints the ticketed download URLs a proxy repository puts into the
// documents it serves.
type Minter struct {
	key    []byte
	prefix string
	now    func() time.Time
}

// NewMinter creates a Minter whose URLs are path-absolute references
// ("<pathPrefix>/terraform/dl/..."): clients resolve them against the URL of
// the document they came in, so no origin is needed. pathPrefix is the path of
// the configured base URL ("" when the server is not published under a path).
// The ticket key is derived from the server HMAC secret; an empty secret
// yields a Minter that mints nothing.
func NewMinter(pathPrefix string, secret []byte) *Minter {
	return &Minter{key: deriveTicketKey(secret), prefix: pathPrefix, now: time.Now}
}

// Enabled reports whether the Minter has a key.
func (m *Minter) Enabled() bool { return len(m.key) != 0 }

// URL returns the ticketed URL of file: a capability for downloading exactly
// the file the kind and coordinates name, valid for the ticket lifetime. It
// returns "" when the Minter has no key.
func (m *Minter) URL(repoID string, kind DownloadKind, a, b, c, file string) string {
	if !m.Enabled() {
		return ""
	}

	t := ticket{repoID: repoID, kind: ticketKind(kind), a: a, b: b, c: c, expires: m.now().Add(ticketTTL)}

	return m.prefix + ticketPath + encodeTicket(m.key, t) + "/" + url.PathEscape(file)
}

// ValidName reports whether s is a namespace, module name, module system or
// provider type of the registry protocols.
func ValidName(s string) bool { return validName(s) }

// ValidVersion reports whether v is a version the registry serves: semver
// without a "v" prefix and without build metadata.
func ValidVersion(v string) bool { return validVersion(v) }

// ValidPlatformPart reports whether s is an os or arch of a provider platform.
func ValidPlatformPart(s string) bool { return validPlatformPart(s) }

// CompareVersions orders two valid versions by semantic-version precedence.
func CompareVersions(a, b string) int { return compareVersions(a, b) }

// LatestVersion picks the version a client gets for "the latest": the highest
// stable version, else the highest one; "" for an empty list.
func LatestVersion(versions []string) string { return latestVersion(versions) }

// ModuleFileName is the file name a module archive is downloaded as.
func ModuleFileName(name, system, version, ext string) string {
	return moduleFileName(name, system, version, ext)
}

// RegistryBasePath is the path a repository is served under.
func RegistryBasePath(repoName string) string { return registryBasePath(repoName) }
