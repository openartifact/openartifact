// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build dev

package web

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

// devHandler proxies the UI to the Vite dev server when OPENARTIFACT_DEV is
// set. It exists only in binaries built with -tags dev: a release binary that
// honoured the variable would let anything listening on localhost:5173 serve
// script under the application origin.
func devHandler() http.Handler {
	if os.Getenv("OPENARTIFACT_DEV") == "" {
		return nil
	}

	log.Printf("web: OPENARTIFACT_DEV is set; proxying UI to http://localhost:5173 (development only)")

	target, _ := url.Parse("http://localhost:5173")

	return httputil.NewSingleHostReverseProxy(target)
}
