// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/pkg/logger"
)

// HTTPServer implements the Server interface.
type HTTPServer struct {
	router      chi.Router
	httpServer  *http.Server
	log         *logger.Logger
	middlewares []func(http.Handler) http.Handler
	tlsCert     string
	tlsKey      string
}

// NewHTTPServer creates a new HTTP server. Defaults favor never truncating
// large artifact transfers: no read/write timeout, with only header reads
// bounded (slowloris mitigation) until SetTimeouts overrides them from config.
func NewHTTPServer(addr string, log *logger.Logger) *HTTPServer {
	r := chi.NewRouter()

	return &HTTPServer{
		router: r,
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           r,
			ReadTimeout:       0,
			WriteTimeout:      0,
			ReadHeaderTimeout: 20 * time.Second,
		},
		log: log,
	}
}

// Start starts the server.
func (s *HTTPServer) Start(ctx context.Context) error {
	s.log.Infow("Starting HTTP server", "address", s.httpServer.Addr)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.httpServer.Addr, err)
	}

	go func() {
		var serveErr error
		if s.tlsCert != "" && s.tlsKey != "" {
			serveErr = s.httpServer.ServeTLS(ln, s.tlsCert, s.tlsKey)
		} else {
			serveErr = s.httpServer.Serve(ln)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.log.Errorw("HTTP server error", "error", serveErr)
		}
	}()

	return nil
}

// EnableTLS makes Start serve HTTPS using the given certificate and key files.
func (s *HTTPServer) EnableTLS(certFile, keyFile string) {
	s.tlsCert, s.tlsKey = certFile, keyFile
	s.httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
}

// Stop gracefully stops the server.
func (s *HTTPServer) Stop(ctx context.Context) error {
	s.log.Infow("Stopping HTTP server")
	return s.httpServer.Shutdown(ctx)
}

// GetRouter returns the underlying chi router.
func (s *HTTPServer) GetRouter() chi.Router {
	return s.router
}

// AddMiddleware adds a middleware to the server.
func (s *HTTPServer) AddMiddleware(middleware func(http.Handler) http.Handler) {
	s.middlewares = append(s.middlewares, middleware)
	s.httpServer.Handler = applyMiddlewares(s.router, s.middlewares...)
}

// applyMiddlewares applies middlewares to a handler.
func applyMiddlewares(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	// Apply middlewares in reverse order so they execute in the order they were added
	for _, m := range slices.Backward(middlewares) {
		handler = m(handler)
	}

	return handler
}

// SetTimeouts configures read, write, and read-header timeouts on the HTTP
// server. readTimeout/writeTimeout of 0 mean "no limit" (relevant for large
// artifact transfers); readHeaderTimeout should stay non-zero regardless —
// it bounds only the header-read phase, so it never truncates a body.
func (s *HTTPServer) SetTimeouts(readTimeout, writeTimeout, readHeaderTimeout time.Duration) {
	s.httpServer.ReadTimeout = readTimeout
	s.httpServer.WriteTimeout = writeTimeout
	s.httpServer.ReadHeaderTimeout = readHeaderTimeout
}
