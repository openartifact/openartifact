// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/netutil"
)

// IPAllowlistMiddleware blocks requests not from allowed CIDRs.
// If allowlist is empty, all IPs are allowed. trusted controls which
// reverse proxies' X-Forwarded-For/X-Real-IP are honored when resolving the
// client IP — see ClientIP.
func IPAllowlistMiddleware(allowedCIDRs []string, trusted *TrustedProxies) func(http.Handler) http.Handler {
	// Config validation rejects unparsable entries at startup. If one still
	// gets here (programmatic use), fail closed on it rather than open: an
	// unparsable entry never widens access, and a non-empty list that yields
	// no networks denies everyone.
	networks, _ := netutil.ParseNetworks(allowedCIDRs)

	if len(networks) == 0 && !hasEntries(allowedCIDRs) {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := ClientIP(r, trusted)
			ip := net.ParseIP(clientIP)

			// Check if IP is in any allowed network
			allowed := false

			if ip != nil {
				for _, network := range networks {
					if network.Contains(ip) {
						allowed = true
						break
					}
				}
			}

			if !allowed {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "IP not allowed"})

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func hasEntries(entries []string) bool {
	for _, e := range entries {
		if strings.TrimSpace(e) != "" {
			return true
		}
	}

	return false
}
