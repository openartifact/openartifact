// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/pprof"
	"runtime"

	"github.com/go-chi/chi/v5"
)

const (
	pprofMutexFraction = 100     // sample 1 in 100 contended mutex events
	pprofBlockRateNs   = 100_000 // sample blocking events of >=100µs on average
)

// registerPprof mounts the runtime profiler on the authenticated router under
// /api/v1/admin/pprof/. Everything below /api/v1/ is authorised as the "manage"
// action by the RBAC middleware (deploy tokens and anonymous callers are
// refused), so only administrators can reach it. Mutex and block profiling,
// which cost a little on every contended lock, are switched on only here.
func registerPprof(r chi.Router) {
	runtime.SetMutexProfileFraction(pprofMutexFraction)
	runtime.SetBlockProfileRate(pprofBlockRateNs)

	const base = "/api/v1/admin/pprof"

	r.Get(base+"/cmdline", pprof.Cmdline)
	r.Get(base+"/profile", pprof.Profile)
	r.Get(base+"/symbol", pprof.Symbol)
	r.Get(base+"/trace", pprof.Trace)
	r.Get(base+"/{name}", func(w http.ResponseWriter, req *http.Request) {
		pprof.Handler(chi.URLParam(req, "name")).ServeHTTP(w, req)
	})
}
