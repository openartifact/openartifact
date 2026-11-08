// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// An SSO PATCH yields a column-wise patch naming only the fields in the body,
// so an edit of one column cannot revert a concurrent edit of another.
func TestBuildSSOPatchNamesOnlyRequestedColumns(t *testing.T) {
	existing := &models.SSOProvider{Kind: "oidc", DisplayName: "old", Enabled: true}

	patch, err := buildSSOPatch(context.Background(), existing, map[string]json.RawMessage{
		"display_name": json.RawMessage(`"new"`),
		"enabled":      json.RawMessage(`false`),
	})
	if err != nil {
		t.Fatalf("buildSSOPatch: %v", err)
	}

	if patch.DisplayName == nil || *patch.DisplayName != "new" || patch.Enabled == nil || *patch.Enabled {
		t.Errorf("display_name/enabled not carried: %+v", patch)
	}

	if patch.AutoProvisionUsers != nil || patch.LinkByEmail != nil || patch.OIDCConfig != nil ||
		patch.SAMLConfig != nil || patch.ClaimMapping != nil || patch.GroupMapping != nil || patch.PermissionMapping != nil {
		t.Errorf("columns absent from the body must stay unset: %+v", patch)
	}
}

func TestBuildSSOPatchRejectsInvalidField(t *testing.T) {
	existing := &models.SSOProvider{Kind: "oidc"}

	if _, err := buildSSOPatch(context.Background(), existing, map[string]json.RawMessage{
		"saml_config": json.RawMessage(`{}`),
	}); err == nil {
		t.Error("saml_config on an oidc provider was accepted")
	}
}
