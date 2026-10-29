// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
)

// The process roles of server.role (OPENARTIFACT_ROLE).
const (
	// RoleAll serves HTTP and runs every background worker: the single-process
	// deployment, and the default.
	RoleAll = "all"
	// RoleWeb serves HTTP and starts no background worker or scheduler; what
	// the workers would do for a request is queued in the database for a
	// RoleWorker (or RoleAll) process.
	RoleWeb = "web"
	// RoleWorker runs the background workers and serves only /health, /ready
	// and /metrics.
	RoleWorker = "worker"
)

var errInvalidRole = errors.New(`server.role must be "all", "web" or "worker"`)

// EffectiveRole is the role with an empty value read as RoleAll.
func (s ServerConfig) EffectiveRole() string {
	if s.Role == "" {
		return RoleAll
	}

	return s.Role
}

// ServesAPI reports whether the process serves the web UI, the REST API and the
// package-format routes (roles all and web).
func (s ServerConfig) ServesAPI() bool { return s.EffectiveRole() != RoleWorker }

// RunsWorkers reports whether the process runs the background workers and
// schedulers (roles all and worker).
func (s ServerConfig) RunsWorkers() bool { return s.EffectiveRole() != RoleWeb }

// validateListen checks the listen port and the role.
func (s ServerConfig) validateListen() error {
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("%w: %d", errInvalidPort, s.Port)
	}

	if err := s.validateLimits(); err != nil {
		return err
	}

	return s.validateRole()
}

const (
	maxServerSeconds   = 24 * 60 * 60
	minMaxHeaderBytes  = 1024
	maxMaxHeaderBytes  = 16 << 20
	maxServerRateLimit = 1_000_000
	maxServerBodyBytes = int64(1) << 50
)

// validateLimits rejects negative or absurd server timeouts and size caps.
func (s ServerConfig) validateLimits() error {
	for _, t := range []struct {
		name string
		v    int
	}{
		{"readTimeout", s.ReadTimeout},
		{"writeTimeout", s.WriteTimeout},
		{"readHeaderTimeout", s.ReadHeaderTimeout},
		{"idleTimeout", s.IdleTimeout},
		{"shutdownTimeout", s.ShutdownTimeout},
		{"shutdownDelay", s.ShutdownDelay},
	} {
		if t.v < 0 || t.v > maxServerSeconds {
			return fmt.Errorf("%w: server.%s=%d, want 0..%d seconds", errInvalidServerLimit, t.name, t.v, maxServerSeconds)
		}
	}

	if s.MaxHeaderBytes != 0 && (s.MaxHeaderBytes < minMaxHeaderBytes || s.MaxHeaderBytes > maxMaxHeaderBytes) {
		return fmt.Errorf("%w: server.maxHeaderBytes=%d, want 0 or %d..%d",
			errInvalidServerLimit, s.MaxHeaderBytes, minMaxHeaderBytes, maxMaxHeaderBytes)
	}

	if s.MaxBodyBytes < 0 || s.MaxBodyBytes > maxServerBodyBytes {
		return fmt.Errorf("%w: server.maxBodyBytes=%d, want 0..%d", errInvalidServerLimit, s.MaxBodyBytes, maxServerBodyBytes)
	}

	if s.RateLimitRPS < 0 || s.RateLimitRPS > maxServerRateLimit {
		return fmt.Errorf("%w: server.rateLimitRps=%d, want 0..%d", errInvalidServerLimit, s.RateLimitRPS, maxServerRateLimit)
	}

	return nil
}

func (s ServerConfig) validateRole() error {
	switch s.EffectiveRole() {
	case RoleAll, RoleWeb, RoleWorker:
		return nil
	default:
		return fmt.Errorf("%w, got %q", errInvalidRole, s.Role)
	}
}
