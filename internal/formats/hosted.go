// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatterraform "github.com/openartifact/openartifact/internal/format/terraform"
	"github.com/openartifact/openartifact/internal/webhook"
)

// The hosted values of the formats whose handler is not a plugin.Hosted on its
// own: it lacks SetPublisher (rpm, deb), the format also has a REST upload
// handler in internal/api, or it takes part in a shared route.

// restUpload is the REST upload handler of internal/api for a format
// (api.RPMUploadHandler and friends): it registers on the authenticated API
// router and announces uploads through the publisher.
type restUpload interface {
	Register(r chi.Router)
	SetPublisher(p webhook.Publisher)
}

// quiet adapts the download-only handlers (rpm, deb), which announce nothing.
type quiet struct {
	routes interface{ RegisterRepoRoutes(r chi.Router) }
}

func (q quiet) RegisterRepoRoutes(r chi.Router) { q.routes.RegisterRepoRoutes(r) }

func (quiet) SetPublisher(webhook.Publisher) {}

// restHosted is a hosted handler together with the REST upload handler of its
// format.
type restHosted struct {
	plugin.Hosted

	rest restUpload
}

// withREST adds the format's REST upload handler, when the server built one.
func withREST(h plugin.Hosted, d *plugin.Deps, format string) plugin.Hosted {
	rest, ok := plugin.REST[restUpload](d, format)
	if !ok {
		return h
	}

	return restHosted{Hosted: h, rest: rest}
}

func (h restHosted) SetPublisher(p webhook.Publisher) {
	h.Hosted.SetPublisher(p)
	h.rest.SetPublisher(p)
}

func (h restHosted) RegisterAPIRoutes(r chi.Router) { h.rest.Register(r) }

// npmPublisher is api.NPMUploadHandler: npm publish and unpublish, on the
// shared repository mount.
type npmPublisher interface {
	RegisterRepoRoutes(r chi.Router)
	HandlePublish(w http.ResponseWriter, r *http.Request)
	SetPublisher(p webhook.Publisher)
}

// npmHosted is the npm read handler. Its guard serves the packuments of the
// package names that would be shadowed by routes of other formats.
type npmHosted struct{ *formatnpm.HTTPHandler }

func (h npmHosted) Guard(next http.Handler) http.Handler { return h.ShadowGuard(next) }

// npmPublishing adds publish and unpublish: its write routes go on the live
// mount only, and it is the catch-all of the shared PUT route.
type npmPublishing struct {
	npmHosted

	publish npmPublisher
}

func (h npmPublishing) SetPublisher(p webhook.Publisher) {
	h.npmHosted.SetPublisher(p)
	h.publish.SetPublisher(p)
}

func (h npmPublishing) RegisterWriteRoutes(r chi.Router) { h.publish.RegisterRepoRoutes(r) }

func (h npmPublishing) PutClaim() plugin.PutClaim {
	return plugin.PutClaim{Serve: h.publish.HandlePublish}
}

// goHosted is the GOPROXY handler: it claims the PUT of a module upload on the
// shared route.
type goHosted struct {
	*formatgomod.HTTPHandler

	serve http.HandlerFunc
}

func (h goHosted) PutClaim() plugin.PutClaim {
	return plugin.PutClaim{Claims: formatgomod.IsUploadPath, Serve: h.serve}
}

// terraformHosted is the Terraform handler; its guard serves the module
// "latest" document.
type terraformHosted struct{ *formatterraform.HTTPHandler }

func (h terraformHosted) Guard(next http.Handler) http.Handler { return h.LatestGuard(next) }

// dockerHosted is the registry handler: the registry protocol is registered
// with RegisterRoutes.
type dockerHosted struct{ *formatdocker.HTTPHandler }

func (h dockerHosted) RegisterRepoRoutes(r chi.Router) { h.RegisterRoutes(r) }
