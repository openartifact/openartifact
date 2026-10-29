// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package plugin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	formatnix "github.com/openartifact/openartifact/internal/format/nix"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Deps are the server services a format's hosted handler is built from. The
// server (cmd/server) fills them once at startup; a format takes what it needs
// and ignores the rest. A field the process has no service for is left zero: a
// nil interface or pointer means the service is absent, and the format skips
// the option that needs it.
type Deps struct {
	Log *logger.Logger
	DB  *database.PostgresDB

	// Served is the blob store downloads are read from (with presigned
	// redirects); Blobs is the one writers use.
	Served storage.BlobStore
	Blobs  storage.BlobStore

	// BaseURL is the BASE_URL override of the origin of the URLs the handlers
	// hand to clients; empty derives it from each request.
	BaseURL string
	Metrics *metrics.Registry
	// HMACKey signs the Terraform download tickets.
	HMACKey []byte

	// DockerUploadDir is where docker blob uploads are staged.
	DockerUploadDir string
	// DockerAuthorizer checks read access to a cross-repository mount source.
	DockerAuthorizer formatdocker.Authorizer

	// APKKeys and NixKeys provide the signing keys of apk and nix repositories.
	APKKeys formatapk.KeyProvider
	NixKeys formatnix.KeyProvider
	// GPG resolves the signer of Terraform repositories; nil when none.
	GPG gpg.Resolver
	// Proxy serves the Terraform tickets of proxy repositories; nil when the
	// process has no proxy service.
	Proxy *proxy.Service

	// Workers are the metadata workers this process runs. Handlers that notify
	// one on upload take it from here.
	Workers *lifecycle.Workers

	// REST are the REST upload handlers of internal/api, by format name. They
	// live in api, which a format registration cannot import, so the server
	// builds them and a format takes its own with REST.
	REST map[string]any

	// CountUploads wraps a handler that serves one format on a route shared with
	// others, counting its uploads in openartifact_uploads_total.
	CountUploads func(format string, next http.HandlerFunc) http.HandlerFunc
}

// REST returns the REST upload handler of format as the narrow interface T the
// format's registration needs. It reports false when the server built none or it
// does not implement T.
func REST[T any](d *Deps, format string) (T, bool) {
	v, ok := d.REST[format].(T)

	return v, ok
}

// Hosted is the protocol handler of a format's hosted repositories.
type Hosted interface {
	// RegisterRepoRoutes registers the format's routes below a repository:
	// relative to /repos/{name}, /repos/<format>/{name}, or the root of the
	// in-memory router groups read hosted members through.
	RegisterRepoRoutes(r chi.Router)
	// SetPublisher sets the webhook publisher the handler announces
	// artifact.uploaded and artifact.deleted through.
	SetPublisher(p webhook.Publisher)
}

// The optional capabilities of a Hosted value. The mounting functions look for
// them with a type assertion, so a format opts in by implementing the method.

// Guarded formats claim requests of the shared /repos/{name} subrouter before
// the routes: paths that would clash with the routes of other formats are
// answered for repositories of the format only and passed on otherwise.
type Guarded interface {
	Guard(next http.Handler) http.Handler
}

// APIRouted formats register routes on the authenticated API router (REST
// upload endpoints under /api/v1/repositories/{name}).
type APIRouted interface {
	RegisterAPIRoutes(r chi.Router)
}

// PublicRouted formats register routes on the bare router, outside
// authentication (a signed URL is the credential).
type PublicRouted interface {
	RegisterPublicRoutes(r chi.Router)
}

// WriteRouted formats register write routes on the live shared mount only, not
// on the read-only view groups read hosted members through (npm publish).
type WriteRouted interface {
	RegisterWriteRoutes(r chi.Router)
}

// PutClaim is a format's claim on the shared PUT /repos/{name}/* route.
type PutClaim struct {
	// Claims reports whether the PUT of tail (the path below the repository) is
	// this format's upload. nil claims every path no other claim took; at most
	// one claim may be the catch-all.
	Claims func(tail string) bool
	// Serve handles the upload.
	Serve http.HandlerFunc
}

// PutRouted formats take part in the shared PUT /repos/{name}/* dispatch:
// several formats upload with a PUT to a path no route can tell apart.
type PutRouted interface {
	PutClaim() PutClaim
}
