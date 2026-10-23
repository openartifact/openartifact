// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// issued is a response the test IdP produced, with the cookies the browser
// would hold for it (the tracker, for SP-initiated logins).
type issued struct {
	resp    string
	relay   string
	cookies []*http.Cookie
}

// issue has idp produce a response: IdP-initiated, or SP-initiated after a real
// login against the SP.
func (e *samlFlowEnv) issue(idp *saml.IdentityProvider, initiated bool) issued {
	e.t.Helper()

	if initiated {
		return issued{resp: e.idpInitiated(idp)}
	}

	loc, tracker := e.login()
	resp, relay := e.idpRespondWith(idp, loc)

	return issued{resp: resp, relay: relay, cookies: []*http.Cookie{tracker}}
}

func (e *samlFlowEnv) post(i issued) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.acs(i.resp, i.relay, i.cookies...)
}

// mutateSAML decodes a SAMLResponse, applies fn to the root element and
// re-encodes it.
func mutateSAML(t *testing.T, b64 string, fn func(root *etree.Element)) string {
	t.Helper()

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}

	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	fn(doc.Root())

	out, err := doc.WriteToBytes()
	if err != nil {
		t.Fatalf("serialize response: %v", err)
	}

	return base64.StdEncoding.EncodeToString(out)
}

func removeSignatures(el *etree.Element, directChildrenOnly bool) {
	for _, child := range el.ChildElements() {
		if child.Tag == "Signature" {
			el.RemoveChild(child)
			continue
		}

		if !directChildrenOnly {
			removeSignatures(child, false)
		}
	}
}

func setText(root *etree.Element, tag, value string) {
	for _, el := range root.FindElements("//" + tag) {
		el.SetText(value)
	}
}

// evilAssertion is a copy of the signed assertion that names a different user
// and carries no signature.
func evilAssertion(t *testing.T, root *etree.Element) *etree.Element {
	t.Helper()

	orig := root.FindElement("./Assertion")
	if orig == nil {
		t.Fatal("response has no Assertion")
	}

	evil := orig.Copy()
	removeSignatures(evil, false)
	evil.CreateAttr("ID", "evil-assertion")
	setText(evil, "NameID", "mallory-nameid")
	setText(evil, "AttributeValue", "mallory")

	return evil
}

// dropKeyInfo removes the certificate a signature carries, leaving the
// verifier to pick the signing certificate from the metadata. KeyInfo is not
// covered by the digest, so the signature itself stays valid.
func dropKeyInfo(el *etree.Element) {
	for _, sig := range el.ChildElements() {
		if sig.Tag != "Signature" {
			continue
		}

		for _, ki := range sig.ChildElements() {
			if ki.Tag == "KeyInfo" {
				sig.RemoveChild(ki)
			}
		}
	}
}

// responseWithoutCert: the Response signature carries no certificate.
func responseWithoutCert(root *etree.Element) { dropKeyInfo(root) }

// assertionWithoutCert: the Response is unsigned and the assertion signature
// carries no certificate.
func assertionWithoutCert(root *etree.Element) {
	removeSignatures(root, true)
	dropKeyInfo(root.FindElement("./Assertion"))
}

// withSAMLTime runs fn with the SAML library's clock shifted by offset.
func withSAMLTime(t *testing.T, offset time.Duration, fn func()) {
	t.Helper()

	prev := saml.TimeNow
	saml.TimeNow = func() time.Time { return time.Now().Add(offset) }

	defer func() { saml.TimeNow = prev }()

	fn()
}

// assertNoLogin fails when a rejected response left any trace of a login.
func assertNoLogin(t *testing.T, env *samlFlowEnv, rw *httptest.ResponseRecorder) {
	t.Helper()

	if hasCookie(rw, auth.SessionCookieName) {
		t.Error("a rejected response must not create a session")
	}

	if env.db.authUsersByName["alice"] != nil || env.db.authUsersByName["mallory"] != nil {
		t.Error("a rejected response must not provision a user")
	}

	if env.db.consumed != 0 {
		t.Error("a rejected response must not consume the assertion")
	}
}

func assertErrorCode(t *testing.T, rw *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	if rw.Code != status {
		t.Fatalf("status = %d, want %d (%s)", rw.Code, status, rw.Body.String())
	}

	var body map[string]any

	_ = json.Unmarshal(rw.Body.Bytes(), &body)

	if body["code"] != code {
		t.Errorf("code = %v, want %s", body["code"], code)
	}
}

// spMetadataOf returns a copy of the SP metadata idp has registered.
func spMetadataOf(idp *saml.IdentityProvider) saml.EntityDescriptor {
	md := *idp.ServiceProviderProvider.(*staticSPProvider).md
	md.SPSSODescriptors = append([]saml.SPSSODescriptor(nil), md.SPSSODescriptors...)

	return md
}

// plainTextIDP is a copy of idp that does not encrypt assertions (the SP
// advertises an encryption key, so by default they are encrypted), which lets a
// test read and rewrite the signed XML.
func plainTextIDP(idp *saml.IdentityProvider) *saml.IdentityProvider {
	md := spMetadataOf(idp)
	for i := range md.SPSSODescriptors {
		md.SPSSODescriptors[i].KeyDescriptors = nil
	}

	cp := *idp
	cp.ServiceProviderProvider = &staticSPProvider{md: &md}

	return &cp
}

// foreignIDP is an IdP with the trusted entity ID but a certificate that is not
// in the provider's metadata.
func foreignIDP(t *testing.T, env *samlFlowEnv) *saml.IdentityProvider {
	t.Helper()

	other := newTestSAMLIdP(t)
	other.ServiceProviderProvider = env.idp.ServiceProviderProvider

	return plainTextIDP(other)
}

// otherAudienceIDP signs with the trusted key but addresses another SP.
func otherAudienceIDP(env *samlFlowEnv) *saml.IdentityProvider {
	md := spMetadataOf(env.plain)
	md.EntityID = "https://other-sp.example.com/metadata"

	cp := *env.plain
	cp.ServiceProviderProvider = &staticSPProvider{md: &md}

	return &cp
}

// wrongEncryptionKeyIDP encrypts the assertion to a key the SP does not hold.
func wrongEncryptionKeyIDP(t *testing.T, env *samlFlowEnv) *saml.IdentityProvider {
	t.Helper()

	md := spMetadataOf(env.idp)
	for i := range md.SPSSODescriptors {
		md.SPSSODescriptors[i].KeyDescriptors = newTestSAMLIdP(t).Metadata().IDPSSODescriptors[0].KeyDescriptors
	}

	cp := *env.idp
	cp.ServiceProviderProvider = &staticSPProvider{md: &md}

	return &cp
}

type samlAttack struct {
	name   string
	status int
	code   string
	issue  func(t *testing.T, e *samlFlowEnv, initiated bool) issued
}

func samlAttacks() []samlAttack {
	plain := func(t *testing.T, e *samlFlowEnv, initiated bool) issued {
		t.Helper()

		return e.issue(e.plain, initiated)
	}

	mutated := func(fn func(*testing.T, *etree.Element)) func(*testing.T, *samlFlowEnv, bool) issued {
		return func(t *testing.T, e *samlFlowEnv, initiated bool) issued {
			t.Helper()

			i := plain(t, e, initiated)
			i.resp = mutateSAML(t, i.resp, func(root *etree.Element) { fn(t, root) })

			return i
		}
	}

	return []samlAttack{
		{"signed by a certificate that is not in the metadata", http.StatusBadRequest, "saml_signature_invalid",
			func(t *testing.T, e *samlFlowEnv, initiated bool) issued {
				t.Helper()

				return e.issue(foreignIDP(t, e), initiated)
			}},
		{"response and assertion unsigned", http.StatusBadRequest, "saml_signature_invalid",
			mutated(func(_ *testing.T, root *etree.Element) { removeSignatures(root, false) })},
		{"signed content tampered after signing", http.StatusBadRequest, "saml_signature_invalid",
			mutated(func(_ *testing.T, root *etree.Element) {
				setText(root, "NameID", "admin-nameid")
				setText(root, "AttributeValue", "mallory")
			})},
		{"unsigned assertion wrapped in front of the signed one", http.StatusBadRequest, "saml_signature_invalid",
			mutated(func(t *testing.T, root *etree.Element) {
				t.Helper()

				root.InsertChildAt(root.FindElement("./Assertion").Index(), evilAssertion(t, root))
			})},
		{"assertion encrypted to a key the SP does not hold", http.StatusBadRequest, "saml_invalid",
			func(t *testing.T, e *samlFlowEnv, initiated bool) issued {
				t.Helper()

				return e.issue(wrongEncryptionKeyIDP(t, e), initiated)
			}},
		{"addressed to another audience", http.StatusBadRequest, "saml_audience_mismatch",
			func(t *testing.T, e *samlFlowEnv, initiated bool) issued {
				t.Helper()

				return e.issue(otherAudienceIDP(e), initiated)
			}},
		{"issued long ago (expired)", http.StatusUnauthorized, "saml_assertion_expired",
			func(t *testing.T, e *samlFlowEnv, initiated bool) issued {
				t.Helper()

				var i issued

				withSAMLTime(e.t, -10*time.Minute, func() { i = e.issue(e.plain, initiated) })

				return i
			}},
	}
}

// Every way of forging or mis-addressing a response is rejected, on both the
// SP-initiated and the IdP-initiated path, without any side effect.
func TestSAML_ACS_RejectsInvalidResponses(t *testing.T) {
	env := newSAMLFlowEnv(t, "attacks", withAllowIDPInitiated)

	for _, mode := range []struct {
		name      string
		initiated bool
	}{{"sp-initiated", false}, {"idp-initiated", true}} {
		for _, atk := range samlAttacks() {
			t.Run(mode.name+"/"+atk.name, func(t *testing.T) {
				rw := env.post(atk.issue(t, env, mode.initiated))

				assertErrorCode(t, rw, atk.status, atk.code)
				assertNoLogin(t, env, rw)
			})
		}
	}
}

// With the Response unsigned only the signed assertion counts. An unsigned
// look-alike placed in front of it is skipped: whoever logs in is the user the
// IdP signed for, never the one the attacker wrote.
func TestSAML_ACS_UnsignedAssertionWrappedInFrontNeverWins(t *testing.T) {
	for _, initiated := range []bool{false, true} {
		env := newSAMLFlowEnv(t, fmt.Sprintf("xsw-%v", initiated), withAllowIDPInitiated)

		i := env.issue(env.plain, initiated)
		i.resp = mutateSAML(t, i.resp, func(root *etree.Element) {
			root.InsertChildAt(root.FindElement("./Assertion").Index(), evilAssertion(t, root))
			removeSignatures(root, true)
		})

		rw := env.post(i)
		if rw.Code != http.StatusFound {
			t.Fatalf("initiated=%v: the signed assertion is still valid, got %d (%s)", initiated, rw.Code, rw.Body.String())
		}

		if env.db.authUsersByName["alice"] == nil {
			t.Errorf("initiated=%v: the user must be the one the IdP signed for", initiated)
		}

		if env.db.authUsersByName["mallory"] != nil {
			t.Errorf("initiated=%v: the unsigned look-alike assertion must never be used", initiated)
		}
	}
}

func TestSAML_ACS_ResponseIssuedInTheFutureRejected(t *testing.T) {
	env := newSAMLFlowEnv(t, "future", withAllowIDPInitiated)

	for _, initiated := range []bool{false, true} {
		var i issued

		withSAMLTime(t, 10*time.Minute, func() { i = env.issue(env.plain, initiated) })

		rw := env.post(i)
		if rw.Code != http.StatusBadRequest && rw.Code != http.StatusUnauthorized {
			t.Errorf("initiated=%v: a response from the future must be rejected, got %d", initiated, rw.Code)
		}

		assertNoLogin(t, env, rw)
	}
}

func TestSAML_IdPInitiated_DisabledByDefault(t *testing.T) {
	env := newSAMLFlowEnv(t, "idp-off")

	rw := env.post(env.issue(env.idp, true))

	assertErrorCode(t, rw, http.StatusBadRequest, "saml_invalid")
	assertNoLogin(t, env, rw)
}

func TestSAML_IdPInitiated_AcceptedWhenEnabled(t *testing.T) {
	env := newSAMLFlowEnv(t, "idp-on", withAllowIDPInitiated)

	tests := []struct {
		name      string
		relay     string
		wantRedir string
	}{
		{"no relay state lands on the home page", "", "/"},
		{"a relative path is honored", "/repositories/maven?tab=1", "/repositories/maven?tab=1"},
		{"an absolute URL is ignored", "https://evil.example.com/phish", "/"},
		{"a scheme-relative URL is ignored", "//evil.example.com/phish", "/"},
		{"a backslash trick is ignored", "/\\evil.example.com", "/"},
		{"a backslash inside the path is ignored", "/ok\\..\\evil", "/"},
		{"a javascript URL is ignored", "javascript:alert(1)", "/"},
		{"a bare host is ignored", "evil.example.com", "/"},
		{"header injection is ignored", "/ok\r\nSet-Cookie: x=y", "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := env.issue(env.idp, true)
			i.relay = tt.relay

			rw := env.post(i)
			if rw.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d (%s)", rw.Code, rw.Body.String())
			}

			if got := rw.Header().Get("Location"); got != tt.wantRedir {
				t.Errorf("Location = %q, want %q", got, tt.wantRedir)
			}

			if !hasCookie(rw, auth.SessionCookieName) {
				t.Error("an accepted IdP-initiated response must start a session")
			}
		})
	}

	if env.db.authUsersByName["alice"] == nil {
		t.Error("expected alice to be provisioned")
	}
}

func TestSAML_IdPInitiated_ReplayRejected(t *testing.T) {
	env := newSAMLFlowEnv(t, "idp-replay", withAllowIDPInitiated)

	i := env.issue(env.idp, true)

	if rw := env.post(i); rw.Code != http.StatusFound {
		t.Fatalf("first use: %d (%s)", rw.Code, rw.Body.String())
	}

	rw := env.post(i)
	assertErrorCode(t, rw, http.StatusUnauthorized, "saml_invalid")

	if hasCookie(rw, auth.SessionCookieName) {
		t.Error("a replayed IdP-initiated response must not create a session")
	}
}

// A response that names a request is never treated as unsolicited, whatever the
// provider setting: stripping or adding InResponseTo must not move a response
// between the two paths.
func TestSAML_IdPInitiated_InResponseToCannotBeBypassed(t *testing.T) {
	env := newSAMLFlowEnv(t, "idp-irt", withAllowIDPInitiated)

	t.Run("an SP-initiated response without its tracker is rejected", func(t *testing.T) {
		i := env.issue(env.idp, false)
		i.cookies = nil

		rw := env.post(i)
		assertErrorCode(t, rw, http.StatusBadRequest, "saml_invalid")
		assertNoLogin(t, env, rw)
	})

	t.Run("InResponseTo stripped from a solicited response", func(t *testing.T) {
		i := env.issue(env.idp, false)
		i.cookies = nil
		i.resp = mutateSAML(t, i.resp, func(root *etree.Element) { root.RemoveAttr("InResponseTo") })

		rw := env.post(i)
		if rw.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", rw.Code, rw.Body.String())
		}

		assertNoLogin(t, env, rw)
	})

	t.Run("InResponseTo added to an unsolicited response", func(t *testing.T) {
		i := env.issue(env.idp, true)
		i.resp = mutateSAML(t, i.resp, func(root *etree.Element) { root.CreateAttr("InResponseTo", "id-made-up") })

		rw := env.post(i)
		if rw.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", rw.Code, rw.Body.String())
		}

		assertNoLogin(t, env, rw)
	})

	t.Run("an assertion that claims to answer a request", func(t *testing.T) {
		// Properly signed, Response/@InResponseTo empty, but the assertion's
		// SubjectConfirmationData names a request.
		sneaky := *env.idp
		sneaky.AssertionMaker = claimsRequestAssertionMaker{}

		rw := env.post(issued{resp: env.idpInitiated(&sneaky)})
		assertErrorCode(t, rw, http.StatusBadRequest, "saml_invalid")
		assertNoLogin(t, env, rw)
	})
}

// claimsRequestAssertionMaker builds a signed assertion whose
// SubjectConfirmationData carries an InResponseTo although the response itself
// has none.
type claimsRequestAssertionMaker struct{}

func (claimsRequestAssertionMaker) MakeAssertion(req *saml.IdpAuthnRequest, session *saml.Session) error {
	req.Request.ID = "id-claimed-request"
	err := saml.DefaultAssertionMaker{}.MakeAssertion(req, session)
	req.Request.ID = ""

	return err
}

// An IdP-initiated response is unrelated to the pending SP-initiated login and
// must not consume (or clear) its tracker.
func TestSAML_IdPInitiated_LeavesPendingLoginTrackerAlone(t *testing.T) {
	env := newSAMLFlowEnv(t, "idp-tracker", withAllowIDPInitiated)

	loc, tracker := env.login()
	spResp, spRelay := env.idpRespond(loc)

	rw := env.acs(env.idpInitiated(env.idp), "", tracker)
	if rw.Code != http.StatusFound {
		t.Fatalf("IdP-initiated login: %d (%s)", rw.Code, rw.Body.String())
	}

	for _, c := range rw.Result().Cookies() {
		if c.Name == env.trackerName() {
			t.Errorf("an IdP-initiated response must not touch the tracker cookie, got Set-Cookie %q (MaxAge %d)", c.Value, c.MaxAge)
		}
	}

	rw = env.acs(spResp, spRelay, tracker)
	if rw.Code != http.StatusFound || rw.Header().Get("Location") != "/dash" {
		t.Fatalf("the pending SP-initiated login must still complete: %d %q (%s)", rw.Code, rw.Header().Get("Location"), rw.Body.String())
	}
}

func inflateSAMLRequest(t *testing.T, location string) string {
	t.Helper()

	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLRequest"))
	if err != nil {
		t.Fatalf("decode SAMLRequest: %v", err)
	}

	out, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("inflate SAMLRequest: %v", err)
	}

	return string(out)
}

// The library default would ask the IdP for transient NameIDs, which change on
// every login and so cannot identify a user.
func TestSAML_AuthnRequest_DoesNotAskForTransientNameID(t *testing.T) {
	env := newSAMLFlowEnv(t, "nameid")

	loc, _ := env.login()

	authn := inflateSAMLRequest(t, loc)
	if strings.Contains(authn, "transient") {
		t.Errorf("AuthnRequest must not request a transient NameID:\n%s", authn)
	}

	rw := env.do(httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/nameid/metadata", nil))
	if strings.Contains(rw.Body.String(), "transient") {
		t.Errorf("SP metadata must not advertise a transient NameID format:\n%s", rw.Body.String())
	}

	// The identity is the NameID the IdP chooses, kept stable across logins.
	if err := xml.Unmarshal([]byte(authn), new(saml.AuthnRequest)); err != nil {
		t.Fatalf("AuthnRequest is not valid XML: %v", err)
	}
}

// IdP certificate rollover: while both the old and the new certificate are in
// the metadata, a response signed by either verifies; one signed by a
// certificate that is not listed never does.
func TestSAML_MultiCertMetadata(t *testing.T) {
	var second *saml.IdentityProvider

	withTwoCerts := func(t *testing.T, idp *saml.IdentityProvider, cfg *models.SAMLConfig) {
		t.Helper()

		second = newTestSAMLIdP(t)

		md := idp.Metadata()
		md.IDPSSODescriptors[0].KeyDescriptors = append(md.IDPSSODescriptors[0].KeyDescriptors, second.Metadata().IDPSSODescriptors[0].KeyDescriptors...)

		b, err := xml.Marshal(md)
		if err != nil {
			t.Fatal(err)
		}

		cfg.IDPMetadataXML = string(b)
	}

	env := newSAMLFlowEnv(t, "rollover", withAllowIDPInitiated, withTwoCerts)
	second.ServiceProviderProvider = env.idp.ServiceProviderProvider
	second = plainTextIDP(second)

	unlisted := foreignIDP(t, env)

	type signer struct {
		name     string
		idp      func() *saml.IdentityProvider
		accepted bool
	}

	type form struct {
		name   string
		mutate func(*etree.Element)
	}

	signers := []signer{
		{"first certificate", func() *saml.IdentityProvider { return env.plain }, true},
		{"second certificate", func() *saml.IdentityProvider { return second }, true},
		{"unlisted certificate", func() *saml.IdentityProvider { return unlisted }, false},
	}
	forms := []form{
		{"certificate embedded", nil},
		{"response signature without certificate", responseWithoutCert},
		{"assertion signature without certificate", assertionWithoutCert},
	}

	type testCase struct {
		name     string
		idp      func() *saml.IdentityProvider
		mutate   func(*etree.Element)
		accepted bool
	}

	var tests []testCase

	for _, sg := range signers {
		for _, f := range forms {
			tests = append(tests, testCase{sg.name + ", " + f.name, sg.idp, f.mutate, sg.accepted})
		}
	}

	for _, initiated := range []bool{false, true} {
		for _, tt := range tests {
			name := "sp-initiated/" + tt.name
			if initiated {
				name = "idp-initiated/" + tt.name
			}

			t.Run(name, func(t *testing.T) {
				i := env.issue(tt.idp(), initiated)
				if tt.mutate != nil {
					i.resp = mutateSAML(t, i.resp, tt.mutate)
				}

				rw := env.post(i)
				if tt.accepted {
					if rw.Code != http.StatusFound || !hasCookie(rw, auth.SessionCookieName) {
						t.Fatalf("expected a login, got %d (%s)", rw.Code, rw.Body.String())
					}

					return
				}

				assertErrorCode(t, rw, http.StatusBadRequest, "saml_signature_invalid")

				if hasCookie(rw, auth.SessionCookieName) {
					t.Error("a response signed by an unlisted certificate must not start a session")
				}
			})
		}
	}
}
