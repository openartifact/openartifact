// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openartifact/openartifact/internal/middleware"
)

const (
	protocolVersionHeader = "Mcp-Protocol-Version"

	// firstNoBatchVersion is the first protocol revision that removed JSON-RPC
	// batching. Revisions are ISO dates, so string comparison orders them.
	firstNoBatchVersion = "2025-06-18"

	// jsonRPCInvalidRequest is the JSON-RPC 2.0 code for a request object that
	// is not valid, which a batch array is on revisions that dropped batching.
	jsonRPCInvalidRequest = -32600
)

// HTTPHandler returns the http.Handler of the MCP endpoint. Mount it behind the
// RBAC middleware: that authenticates the bearer credential and refuses
// anonymous callers; the handler re-checks it (a defense in depth that also
// hands the principal to the tools) and then applies, in order, the per-caller
// rate limit, the body size cap and the batch refusal before the SDK's
// stateless Streamable HTTP transport runs.
func (s *Server) HTTPHandler() http.Handler {
	transport := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return s.mcpServer },
		&sdk.StreamableHTTPOptions{
			// Stateless: no session store, every request is self-contained and
			// authorized on its own, as the 2026-07-28 revision requires. Older
			// clients that send initialize are served through an ephemeral session.
			Stateless: true,
			// Plain JSON replies; nothing here streams.
			JSONResponse:        true,
			MaxRequestBodyBytes: s.maxBodyBytes,
		},
	)

	var h http.Handler = transport

	h = s.guardBody(h)
	h = s.rateLimit(h)

	return auth.RequireBearerToken(verifyCaller, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: s.resourceMetadataURL,
	})(h)
}

// rateLimit rejects callers above their per-minute budget with 429.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := middleware.PrincipalFromContext(r.Context()); p != nil && !s.requestLimiter.allow(principalKey(p)) {
			w.Header().Set("Retry-After", strconv.Itoa(int(s.requestLimiter.retryAfter().Seconds())+1))
			writeRPCError(w, http.StatusTooManyRequests, jsonRPCServerError, "rate limit exceeded")

			return
		}

		next.ServeHTTP(w, r)
	})
}

// jsonRPCServerError is the implementation-defined server error code used for
// transport-level refusals that have no more specific JSON-RPC code.
const jsonRPCServerError = -32000

// guardBody enforces the request body cap and refuses JSON-RPC batch arrays on
// protocol revisions that removed batching (answering -32600). The body is
// buffered (bounded by the cap) so the SDK still reads it afterwards.
func (s *Server) guardBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		if r.ContentLength > s.maxBodyBytes {
			writeRPCError(w, http.StatusRequestEntityTooLarge, jsonRPCServerError, "request body too large")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBodyBytes))
		if err != nil {
			if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
				writeRPCError(w, http.StatusRequestEntityTooLarge, jsonRPCServerError, "request body too large")
				return
			}

			writeRPCError(w, http.StatusBadRequest, jsonRPCInvalidRequest, "cannot read request body")

			return
		}

		if version := r.Header.Get(protocolVersionHeader); version >= firstNoBatchVersion && isBatch(body) {
			writeRPCError(w, http.StatusBadRequest, jsonRPCInvalidRequest,
				"JSON-RPC batch requests are not supported by protocol version "+version)

			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// isBatch reports whether the JSON body is an array.
func isBatch(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")

	return len(trimmed) > 0 && trimmed[0] == '['
}

// rpcErrorBody is a JSON-RPC error response; the id is null because the
// request was refused before it was parsed.
type rpcErrorBody struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"` // always null: refused before parsing
	Error   rpcErrorPayload `json:"error"`
}

type rpcErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPCError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if encErr := json.NewEncoder(w).Encode(rpcErrorBody{JSONRPC: "2.0", Error: rpcErrorPayload{Code: code, Message: message}}); encErr != nil {
		_ = encErr // response header already written
	}
}

// OriginMiddleware refuses browser requests to the MCP endpoint whose Origin is
// not allowed (403), the DNS-rebinding defense the Streamable HTTP transport
// requires. Requests without an Origin header (CLIs, desktop agents) pass; so
// does everything outside mcpPath. Allowed origins are the base URL's origin
// plus extra, compared case-insensitively without a trailing slash.
func OriginMiddleware(mcpPath, baseURL string, extra []string) func(http.Handler) http.Handler {
	allowed := map[string]struct{}{}

	for _, o := range append([]string{originOf(baseURL)}, extra...) {
		if n := normalizeOrigin(o); n != "" {
			allowed[n] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" && onPath(r.URL.Path, mcpPath) {
				if _, ok := allowed[normalizeOrigin(origin)]; !ok {
					writeRPCError(w, http.StatusForbidden, jsonRPCServerError, "origin not allowed")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func onPath(urlPath, base string) bool {
	return urlPath == base || strings.HasPrefix(urlPath, base+"/")
}

// originOf returns scheme://host of a URL, or "" if it has none.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}

	return u.Scheme + "://" + u.Host
}

func normalizeOrigin(o string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
}
