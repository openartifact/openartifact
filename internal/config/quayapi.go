// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	quayOrgNameRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$`)
	errQuayOrgInvalid = errors.New("quayApi.organizations: not a repository name")
)

// QuayAPIConfig configures the Quay-compatible API facade
// (docs/integrations/konflux-image-controller.md): the Konflux image-controller
// onboards components through it as if OpenArtifact were a Quay registry.
type QuayAPIConfig struct {
	// Enabled mounts the facade at /quay/api/v1. Off by default: it hands out
	// registry credentials to a token with global manage permission.
	Enabled bool `yaml:"enabled"`
	// Organizations lists the hosted docker repositories a Quay client may use
	// as its organization. Empty allows every hosted docker repository.
	Organizations []string `yaml:"organizations"`
}

func defaultQuayAPIConfig() QuayAPIConfig { return QuayAPIConfig{} }

func (c QuayAPIConfig) validate() error {
	for _, org := range c.Organizations {
		if !quayOrgNameRe.MatchString(org) {
			return fmt.Errorf("%w: %q", errQuayOrgInvalid, org)
		}
	}

	return nil
}
