// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/quayapi"
)

var errQuayNeedsSecretKey = errors.New("quayApi.enabled requires " + crypto.EnvKey +
	": the Quay API hands a robot's token out again, so robot tokens are stored sealed")

// quayAPIPath is the RBAC prefix of the Quay-compatible API facade, "" while
// quayApi is disabled.
func (a *app) quayAPIPath() string {
	if !a.cfg.QuayAPI.Enabled {
		return ""
	}

	return quayapi.Prefix
}

// registerQuayAPI mounts the Quay-compatible API facade for the Konflux
// image-controller when quayApi.enabled is set.
func (a *app) registerQuayAPI() error {
	if !a.cfg.QuayAPI.Enabled {
		return nil
	}

	if !crypto.Enabled() {
		return a.fail("invalid quayApi configuration", errQuayNeedsSecretKey)
	}

	quayapi.New(a.db, a.web.evaluator, a.log, a.cfg.QuayAPI.Organizations).
		WithPublisher(a.workers.webhook).
		Register(a.web.authed)

	a.log.Infow("Quay-compatible API enabled", "path", quayapi.Prefix, "organizations", a.cfg.QuayAPI.Organizations)

	return nil
}
