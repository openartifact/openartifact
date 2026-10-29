// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package plugin

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/webhook"
)

// ErrManyCatchAlls is returned when more than one format claims every path of
// the shared PUT route.
var ErrManyCatchAlls = errors.New("more than one format claims the shared PUT catch-all")

// BuildHosted builds the hosted handler of every registered format.
func BuildHosted(d *Deps) (map[string]Hosted, error) {
	hosted := make(map[string]Hosted)

	for _, f := range All() {
		h, err := f.Hosted(d)
		if err != nil {
			return nil, fmt.Errorf("build %s handler: %w", f.Name(), err)
		}

		hosted[f.Name()] = h
	}

	return hosted, nil
}

// ReadRouters returns an in-memory router per format with the hosted handler's
// read routes registered. Proxy and group repositories are served by the
// dispatcher in front of the per-format routes; hosted repositories fall
// through to them, and groups read their hosted members through these routers.
func ReadRouters(hosted map[string]Hosted) map[string]http.Handler {
	routers := make(map[string]http.Handler, len(hosted))

	for name, h := range hosted {
		r := chi.NewRouter()
		h.RegisterRepoRoutes(r)

		routers[name] = r
	}

	return routers
}

// SetPublisher gives every hosted handler the publisher it announces uploads
// and deletions through.
func SetPublisher(hosted map[string]Hosted, p webhook.Publisher) {
	for _, h := range hosted {
		h.SetPublisher(p)
	}
}

// RegisterAPIRoutes registers the REST upload endpoints of the formats that have
// them on the authenticated API router. Call it after MountIsolated: a route
// registered on a path an isolated mount also serves would otherwise be
// overridden by it.
func RegisterAPIRoutes(authed chi.Router, hosted map[string]Hosted) {
	for _, f := range All() {
		if h, ok := hosted[f.Name()].(APIRouted); ok {
			h.RegisterAPIRoutes(authed)
		}
	}
}

// RegisterPublicRoutes registers the routes that need no authentication.
func RegisterPublicRoutes(router chi.Router, hosted map[string]Hosted) {
	for _, f := range All() {
		if h, ok := hosted[f.Name()].(PublicRouted); ok {
			h.RegisterPublicRoutes(router)
		}
	}
}

// MountShared registers the formats served under /repos/{name} on the single
// subrouter, behind the dispatcher middleware, and dispatches the shared PUT
// catch-all among the formats that claim it.
func MountShared(authed chi.Router, hosted map[string]Hosted, dispatch func(http.Handler) http.Handler) error {
	claims, err := putClaims(hosted)
	if err != nil {
		return err
	}

	authed.Route("/repos/{name}", func(r chi.Router) {
		r.Use(dispatch)

		for _, f := range All() {
			if g, ok := hosted[f.Name()].(Guarded); ok && sharedMount(f.Name()) {
				r.Use(g.Guard)
			}
		}

		for _, f := range All() {
			if repotype.MountOf(f.Name()) == repotype.MountShared {
				hosted[f.Name()].RegisterRepoRoutes(r)
			}
		}

		for _, f := range All() {
			if h, ok := hosted[f.Name()].(WriteRouted); ok && sharedMount(f.Name()) {
				h.RegisterWriteRoutes(r)
			}
		}

		if len(claims) > 0 {
			r.Put("/*", func(w http.ResponseWriter, req *http.Request) {
				tail := chi.URLParam(req, "*")

				for _, c := range claims {
					if c.Claims == nil || c.Claims(tail) {
						c.Serve(w, req)
						return
					}
				}

				http.NotFound(w, req)
			})
		}
	})

	return nil
}

// sharedMount reports whether the format is served on the shared subrouter,
// through its routes or its guard.
func sharedMount(name string) bool {
	m := repotype.MountOf(name)

	return m == repotype.MountShared || m == repotype.MountGuarded
}

// putClaims collects the claims on the shared PUT route: the claims with a
// matcher in format order, the catch-all last.
func putClaims(hosted map[string]Hosted) ([]PutClaim, error) {
	var matched []PutClaim

	var catchAll *PutClaim

	for _, f := range All() {
		h, ok := hosted[f.Name()].(PutRouted)
		if !ok {
			continue
		}

		c := h.PutClaim()
		switch {
		case c.Claims != nil:
			matched = append(matched, c)
		case catchAll != nil:
			return nil, fmt.Errorf("%s: %w", f.Name(), ErrManyCatchAlls)
		default:
			catchAll = &c
		}
	}

	if catchAll != nil {
		matched = append(matched, *catchAll)
	}

	return matched, nil
}

// MountIsolated registers the formats whose wildcard routes would shadow others
// on /repos/{name}, each on a prefix of its own, /repos/<format>/{name}.
func MountIsolated(authed chi.Router, hosted map[string]Hosted, dispatch func(http.Handler) http.Handler) {
	for _, f := range All() {
		if repotype.MountOf(f.Name()) != repotype.MountIsolated {
			continue
		}

		h := hosted[f.Name()]

		authed.Route("/repos/"+f.Name()+"/{name}", func(r chi.Router) {
			r.Use(dispatch)
			h.RegisterRepoRoutes(r)
		})
	}
}

// MountOCI registers the registry protocol (/v2/...) behind its dispatcher
// middleware: proxy and group repositories are served there, hosted ones by the
// format's handler.
func MountOCI(authed chi.Router, hosted map[string]Hosted, dispatch func(http.Handler) http.Handler) {
	for _, f := range All() {
		if repotype.MountOf(f.Name()) != repotype.MountOCI {
			continue
		}

		h := hosted[f.Name()]

		authed.Group(func(r chi.Router) {
			r.Use(dispatch)
			h.RegisterRepoRoutes(r)
		})
	}
}
