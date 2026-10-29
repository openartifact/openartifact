// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package plugin

import (
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
)

// Definition is the table form of a Format: the registration of a format that
// needs no behaviour of its own beyond the values below. Every field is
// required; a behaviour the format does not have is its explicit zero value
// (NoProxy, NoGroup, NoAnalysis, lifecycle.None).
type Definition struct {
	// ID is the format name (Format.Name).
	ID string
	// NewHosted builds the hosted handler (Format.Hosted).
	NewHosted func(*Deps) (Hosted, error)
	// Supply is the supply-chain analysis (Format.Analysis).
	Supply Analysis
	// Snippets is the client setup builder (Format.ClientSetup).
	Snippets clientsetup.Builder
	// Mirror is the proxy behaviour (Format.Proxy).
	Mirror Proxy
	// Merge is the group behaviour (Format.Group).
	Merge Group
	// Meta is the metadata lifecycle (Format.Lifecycle).
	Meta lifecycle.Hooks
}

// Name implements Format.
func (d Definition) Name() string { return d.ID }

// Hosted implements Format.
func (d Definition) Hosted(deps *Deps) (Hosted, error) {
	if d.NewHosted == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoHostedHandler, d.ID)
	}

	return d.NewHosted(deps)
}

// ErrNoHostedHandler is returned by a Definition that has no NewHosted.
var ErrNoHostedHandler = errors.New("plugin: format has no hosted handler constructor")

// Analysis implements Format.
func (d Definition) Analysis() Analysis { return d.Supply }

// ClientSetup implements Format.
func (d Definition) ClientSetup() clientsetup.Builder { return d.Snippets }

// Proxy implements Format.
func (d Definition) Proxy() Proxy { return d.Mirror }

// Group implements Format.
func (d Definition) Group() Group { return d.Merge }

// Lifecycle implements Format.
func (d Definition) Lifecycle() lifecycle.Hooks { return d.Meta }
