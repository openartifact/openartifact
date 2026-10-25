// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/crewjam/saml"
	"github.com/openartifact/openartifact/internal/database/models"
)

func samlAttr(name string, values ...string) saml.Attribute {
	a := saml.Attribute{Name: name}
	for _, v := range values {
		a.Values = append(a.Values, saml.AttributeValue{Value: v})
	}

	return a
}

func samlAssertionWith(attrs ...saml.Attribute) *saml.Assertion {
	return &saml.Assertion{AttributeStatements: []saml.AttributeStatement{{Attributes: attrs}}}
}

func TestReadSAMLAttributes_GroupsPresence(t *testing.T) {
	t.Parallel()

	cfg := models.SAMLConfig{GroupsAttribute: "memberOf"}

	tests := []struct {
		name        string
		cfg         models.SAMLConfig
		attrs       []saml.Attribute
		wantPresent bool
		wantGroups  []string
	}{
		{
			name:        "attribute with values is a present set",
			cfg:         cfg,
			attrs:       []saml.Attribute{samlAttr("memberOf", "admins", "devs")},
			wantPresent: true,
			wantGroups:  []string{"admins", "devs"},
		},
		{
			name:        "attribute with no values is a present, empty set",
			cfg:         cfg,
			attrs:       []saml.Attribute{samlAttr("memberOf")},
			wantPresent: true,
		},
		{
			name:        "blank values are dropped but the set is still present",
			cfg:         cfg,
			attrs:       []saml.Attribute{samlAttr("memberOf", "", "devs")},
			wantPresent: true,
			wantGroups:  []string{"devs"},
		},
		{
			name:  "attribute absent says nothing about groups",
			cfg:   cfg,
			attrs: []saml.Attribute{samlAttr("uid", "alice")},
		},
		{
			name:  "no groups_attribute configured never counts as present",
			cfg:   models.SAMLConfig{},
			attrs: []saml.Attribute{samlAttr("memberOf", "admins")},
		},
		{
			name: "attributes match by Name, not FriendlyName",
			cfg:  cfg,
			attrs: []saml.Attribute{{
				Name: "urn:oid:1.2.3", FriendlyName: "memberOf",
				Values: []saml.AttributeValue{{Value: "admins"}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := readSAMLAttributes(samlAssertionWith(tt.attrs...), tt.cfg)
			if got.groupsPresent != tt.wantPresent {
				t.Errorf("groupsPresent = %v, want %v", got.groupsPresent, tt.wantPresent)
			}

			if !slices.Equal(got.groups, tt.wantGroups) {
				t.Errorf("groups = %v, want %v", got.groups, tt.wantGroups)
			}
		})
	}
}

func TestReadSAMLAttributes_Mapping(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()

		got := readSAMLAttributes(samlAssertionWith(
			samlAttr("uid", "alice"), samlAttr("mail", "a@example.com"), samlAttr("displayName", "Alice"),
			samlAttr("email_verified", "true"),
		), models.SAMLConfig{})

		if got.username != "alice" || got.email != "a@example.com" || got.fullName != "Alice" || got.emailVerified != "true" {
			t.Errorf("unexpected attributes: %+v", got)
		}
	})

	t.Run("one attribute feeds username and email", func(t *testing.T) {
		t.Parallel()

		got := readSAMLAttributes(samlAssertionWith(samlAttr("mail", "a@example.com")),
			models.SAMLConfig{UsernameAttribute: "mail", EmailAttribute: "mail"})

		if got.username != "a@example.com" || got.email != "a@example.com" {
			t.Errorf("both fields must be set from the shared attribute: %+v", got)
		}
	})

	t.Run("email is unverified unless the IdP says true", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			value string
			want  bool
		}{{"true", true}, {" TRUE ", true}, {"false", false}, {"1", false}, {"", false}} {
			if got := samlEmailVerified(tc.value); got != tc.want {
				t.Errorf("samlEmailVerified(%q) = %v, want %v", tc.value, got, tc.want)
			}
		}
	})
}

func TestValidateSAMLConfig_RefreshSeconds(t *testing.T) {
	t.Parallel()

	const xmlDoc = `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com">` +
		`<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">` +
		`<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.com/sso"/>` +
		`</IDPSSODescriptor></EntityDescriptor>`

	tests := []struct {
		name    string
		seconds int
		wantErr bool
	}{
		{"unset means default", 0, false},
		{"minimum", 60, false},
		{"default value", 3600, false},
		{"maximum", 86400, false},
		{"below minimum", 59, true},
		{"negative", -1, true},
		{"above maximum", 86401, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSAMLConfig(t.Context(), &models.SAMLConfig{IDPMetadataXML: xmlDoc, IDPMetadataRefreshSeconds: tt.seconds})
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSAMLConfig(%d) error = %v, wantErr %v", tt.seconds, err, tt.wantErr)
			}

			if err != nil && !strings.Contains(err.Error(), "idp_metadata_refresh_seconds") {
				t.Errorf("error should name the field, got %v", err)
			}
		})
	}
}

func TestMergeSAMLPatch_NewFields(t *testing.T) {
	t.Parallel()

	stored, err := json.Marshal(models.SAMLConfig{
		IDPMetadataXML: `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com">` +
			`<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">` +
			`<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.com/sso"/>` +
			`</IDPSSODescriptor></EntityDescriptor>`,
		GroupsAttribute: "memberOf",
	})
	if err != nil {
		t.Fatal(err)
	}

	merged, err := mergeSAMLPatch(t.Context(), stored, json.RawMessage(`{"allow_idp_initiated":true,"idp_metadata_refresh_seconds":120}`))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	var got models.SAMLConfig
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}

	if !got.AllowIDPInitiated || got.IDPMetadataRefreshSeconds != 120 {
		t.Errorf("patch not applied: %+v", got)
	}

	if got.GroupsAttribute != "memberOf" || got.IDPMetadataXML == "" {
		t.Errorf("unpatched fields must be kept: %+v", got)
	}

	// Omitting the flag keeps it; setting it false turns it back off.
	merged, err = mergeSAMLPatch(t.Context(), merged, json.RawMessage(`{"groups_attribute":"roles"}`))
	if err != nil {
		t.Fatal(err)
	}

	got = models.SAMLConfig{}
	_ = json.Unmarshal(merged, &got)

	if !got.AllowIDPInitiated {
		t.Error("a patch that omits allow_idp_initiated must keep it")
	}

	merged, err = mergeSAMLPatch(t.Context(), merged, json.RawMessage(`{"allow_idp_initiated":false}`))
	if err != nil {
		t.Fatal(err)
	}

	got = models.SAMLConfig{}
	_ = json.Unmarshal(merged, &got)

	if got.AllowIDPInitiated {
		t.Error("allow_idp_initiated=false must turn the flag off")
	}

	if _, err := mergeSAMLPatch(t.Context(), merged, json.RawMessage(`{"idp_metadata_refresh_seconds":5}`)); err == nil {
		t.Error("an out-of-range refresh interval must be rejected")
	}
}
