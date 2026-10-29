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

	return s.validateRole()
}

func (s ServerConfig) validateRole() error {
	switch s.EffectiveRole() {
	case RoleAll, RoleWeb, RoleWorker:
		return nil
	default:
		return fmt.Errorf("%w, got %q", errInvalidRole, s.Role)
	}
}
