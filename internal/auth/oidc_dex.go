// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"regexp"
)

// Dex scopes OpenArtifact asks for when a provider is pinned to a connector.
const (
	// DexGroupsScope makes Dex release the groups claim.
	DexGroupsScope = "groups"
	// DexFederatedIDScope makes Dex release federated_claims (connector_id
	// and the upstream user_id) in the ID token.
	DexFederatedIDScope = "federated:id"
)

// dexConnectorIDPattern is the charset of a Dex connector id: it ends up in
// the authorization URL and in an ID token claim comparison, so it is kept to
// plain identifier characters.
var dexConnectorIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidDexConnectorID reports whether id is usable as dex_connector_id.
func ValidDexConnectorID(id string) bool {
	return dexConnectorIDPattern.MatchString(id)
}

// checkDexConnector enforces dex_connector_id: the ID token must say it was
// issued for a login through that connector. The connector_id request
// parameter is only a hint the browser can drop, so without this check a user
// could authenticate through any other connector of the same Dex and land on
// this provider (and its group mapping). A provider without dex_connector_id
// is not checked.
func (p *OIDCProvider) checkDexConnector(claims map[string]any) error {
	want := p.Cfg.DexConnectorID
	if want == "" {
		return nil
	}

	fed, ok := claims["federated_claims"].(map[string]any)
	if !ok {
		return denied("dex_connector_id %q is set but the ID token has no federated_claims (is the federated:id scope granted?)", want)
	}

	got, _ := fed["connector_id"].(string)
	if got != want {
		return denied("ID token comes from Dex connector %q, the provider is pinned to %q", got, want)
	}

	return nil
}
