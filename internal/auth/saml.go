// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

var (
	ErrIDPMetadataUnavailable = errors.New("idp metadata unavailable")
	// ErrSAMLSignatureInvalid covers a signature that does not verify
	// (tampered, foreign certificate, wrapped assertion, unusable metadata
	// certificate) and, through ErrSAMLSignatureMissing, a response with no
	// signature where one is required.
	ErrSAMLSignatureInvalid = errors.New("saml signature invalid")
	// ErrSAMLSignatureMissing means neither the response nor the assertion is
	// signed. It matches ErrSAMLSignatureInvalid too.
	ErrSAMLSignatureMissing        = fmt.Errorf("%w: response and assertion are unsigned", ErrSAMLSignatureInvalid)
	ErrSAMLAudienceMismatch        = errors.New("saml audience mismatch")
	ErrSAMLAssertionExpired        = errors.New("saml assertion expired")
	ErrSAMLAssertionNotYetValid    = errors.New("saml assertion not yet valid")
	ErrSAMLRequestMismatch         = errors.New("saml InResponseTo does not match the login request")
	ErrSAMLDestinationMismatch     = errors.New("saml destination or recipient mismatch")
	ErrSAMLIssuerMismatch          = errors.New("saml issuer mismatch")
	ErrSAMLBadStatus               = errors.New("saml response status is not success")
	errSAMLUnsolicitedInResponseTo = errors.New("idp-initiated assertion carries an InResponseTo")
)

//nolint:gochecknoglobals // process-lifetime SAML provider cache, keyed by slug
var samlCache sync.Map

type SAMLProvider struct {
	Slug string
	Cfg  models.SAMLConfig
	// SP is the strict service provider: it only accepts a response to an
	// AuthnRequest tracked by StartLogin.
	SP       *samlsp.Middleware
	EntityID string
	AcsURL   string

	// idpInitiatedSP accepts unsolicited responses (no InResponseTo). It is
	// non-nil only when Cfg.AllowIDPInitiated is set.
	idpInitiatedSP *saml.ServiceProvider

	// fingerprint identifies the row version, IdP metadata and SP identity this
	// provider was built from; a mismatch means the cached entry is stale.
	fingerprint string
}

// LoadSAMLProvider loads an enabled SAML provider. Disabled providers are
// rejected with ErrSSOProviderDisabled.
func LoadSAMLProvider(ctx context.Context, db database.Database, slug, baseURL string, appCfg *config.Config, log *logger.Logger) (*SAMLProvider, error) {
	row, err := db.GetSSOProviderBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("get sso provider: %w", err)
	}

	if row == nil {
		return nil, ErrSSOProviderNotFound
	}

	if !row.Enabled {
		return nil, ErrSSOProviderDisabled
	}

	return LoadSAMLProviderRow(ctx, db, row, baseURL, appCfg, log)
}

// LoadSAMLProviderRow builds the provider for an already-fetched row without
// checking Enabled; it exists so admins can test-connect a disabled provider.
// Public login paths MUST use LoadSAMLProvider. store persists the generated SP
// keypair when none is configured.
func LoadSAMLProviderRow(ctx context.Context, store SettingsStore, row *models.SSOProvider, baseURL string, appCfg *config.Config, log *logger.Logger) (*SAMLProvider, error) {
	slug := row.Slug

	if row.Kind != "saml" {
		return nil, fmt.Errorf("provider %q is not a saml provider (kind=%s)", slug, row.Kind) //nolint:err113 // dynamic error with runtime context
	}

	var samlCfg models.SAMLConfig
	if err := json.Unmarshal(row.SAMLConfig, &samlCfg); err != nil {
		return nil, fmt.Errorf("unmarshal saml_config: %w", err)
	}

	entityID := baseURL + "/api/v1/auth/sso/saml/" + slug + "/metadata"
	acsURL := baseURL + "/api/v1/auth/sso/saml/" + slug + "/acs"

	idpMeta, err := samlMetadata.metadata(ctx, samlCfg, log)
	if err != nil {
		return nil, err
	}

	fingerprint, err := samlFingerprint(row, idpMeta, entityID)
	if err != nil {
		return nil, err
	}

	if cached, ok := samlCache.Load(slug); ok {
		if p, ok := cached.(*SAMLProvider); ok && p.fingerprint == fingerprint {
			return p, nil
		}
	}

	rsaKey, cert, err := loadOrGenerateSAMLKeypair(ctx, store, appCfg, log)
	if err != nil {
		return nil, fmt.Errorf("load or generate sp keypair: %w", err)
	}

	acsURLParsed, err := url.Parse(acsURL)
	if err != nil {
		return nil, fmt.Errorf("parse acs url: %w", err)
	}

	metadataURL, err := url.Parse(entityID)
	if err != nil {
		return nil, fmt.Errorf("parse metadata url: %w", err)
	}

	// An IdP without a usable signing certificate can still start a login;
	// every response is then rejected as unverifiable.
	signingCerts, certErr := idpSigningCerts(idpMeta)
	if certErr != nil && log != nil {
		log.Warnw("SAML IdP metadata has no usable signing certificate; responses cannot be verified", "slug", slug, "error", certErr)
	}

	sp, err := samlsp.New(samlsp.Options{
		EntityID:    entityID,
		URL:         *acsURLParsed,
		Key:         rsaKey,
		Certificate: cert,
		IDPMetadata: idpMeta,
		// Strict: every response must answer a tracked AuthnRequest.
		AllowIDPInitiated: false,
	})
	if err != nil {
		return nil, fmt.Errorf("create saml middleware: %w", err)
	}

	sp.ServiceProvider.AcsURL = *acsURLParsed
	sp.ServiceProvider.MetadataURL = *metadataURL
	// Without NameIDPolicy/@Format the IdP uses the NameID format configured for
	// this application; the library default would ask for transient NameIDs,
	// which change on every login and cannot identify a user.
	sp.ServiceProvider.AuthnNameIDFormat = saml.UnspecifiedNameIDFormat

	if len(signingCerts) > 0 {
		sp.ServiceProvider.SignatureVerifier = &idpCertVerifier{certs: signingCerts}
	}

	p := &SAMLProvider{
		Slug:        slug,
		Cfg:         samlCfg,
		SP:          sp,
		EntityID:    entityID,
		AcsURL:      acsURL,
		fingerprint: fingerprint,
	}

	if samlCfg.AllowIDPInitiated {
		p.idpInitiatedSP = newIDPInitiatedSP(&sp.ServiceProvider)
	}

	samlCache.Store(slug, p)

	return p, nil
}

// newIDPInitiatedSP derives the service provider for unsolicited responses from
// the strict one. Everything else about validation (signature, audience,
// Destination, Recipient, time window, issuer) is unchanged; only the request
// binding differs: the response must carry no InResponseTo at all, and the
// caller additionally checks the assertion's SubjectConfirmationData.
func newIDPInitiatedSP(strict *saml.ServiceProvider) *saml.ServiceProvider {
	sp := *strict
	sp.AllowIDPInitiated = true
	sp.ValidateRequestID = func(resp saml.Response, _ []string) error {
		if resp.InResponseTo != "" {
			return errSAMLUnsolicitedInResponseTo
		}

		return nil
	}

	return &sp
}

// samlFingerprint hashes everything a cached provider is derived from: the row
// version, the (freshly fetched) IdP metadata, the attribute mapping and the SP
// entity ID. Any change yields a different fingerprint, so a rotated or revoked
// IdP certificate or an edited configuration is never served stale. The
// metadata's validity hints are left out: IdPs that restamp validUntil on every
// fetch would otherwise rebuild the provider on every refresh.
func samlFingerprint(row *models.SSOProvider, idpMeta *saml.EntityDescriptor, entityID string) (string, error) {
	stable := *idpMeta
	stable.ValidUntil = time.Time{}
	stable.CacheDuration = 0

	metaXML, err := xml.Marshal(&stable)
	if err != nil {
		return "", fmt.Errorf("marshal idp metadata: %w", err)
	}

	h := sha256.New()
	h.Write([]byte(row.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte{0})
	h.Write([]byte(entityID))
	h.Write([]byte{0})
	h.Write(row.SAMLConfig)
	h.Write([]byte{0})
	h.Write(metaXML)

	return hex.EncodeToString(h.Sum(nil)), nil
}

const (
	samlRequestCookiePrefix = "oa_saml_req_"
	samlRequestTTL          = 10 * time.Minute
	// samlReplayRetention is added to an assertion's own validity window when
	// recording it, so the replay cache outlives every moment the assertion
	// could still pass time-validation (including clock skew).
	samlReplayRetention = time.Minute
)

var (
	ErrSAMLRequestUntracked = errors.New("saml response does not match a pending login request")
	ErrSAMLReplay           = errors.New("saml assertion already used")
	ErrSAMLTrackerKey       = errors.New("saml request tracker key not configured")
)

func samlRequestCookieName(slug string) string { return samlRequestCookiePrefix + slug }

// signSAMLRequestID returns "<payload>.<mac>" binding the AuthnRequest ID to the
// provider slug and an expiry under key.
func signSAMLRequestID(key []byte, slug, requestID string, expires time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(requestID + "|" + strconv.FormatInt(expires.Unix(), 10)))

	return payload + "." + samlRequestMAC(key, slug, payload)
}

func samlRequestMAC(key []byte, slug, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("oa-saml-req|" + slug + "|" + payload))

	return hex.EncodeToString(mac.Sum(nil))
}

// verifySAMLRequestID validates a tracker cookie value and returns the request ID.
func verifySAMLRequestID(key []byte, slug, value string, now time.Time) (string, error) {
	payload, gotMAC, ok := strings.Cut(value, ".")
	if !ok || !hmac.Equal([]byte(gotMAC), []byte(samlRequestMAC(key, slug, payload))) {
		return "", ErrSAMLRequestUntracked
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", ErrSAMLRequestUntracked
	}

	id, expStr, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return "", ErrSAMLRequestUntracked
	}

	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() > exp {
		return "", ErrSAMLRequestUntracked
	}

	return id, nil
}

func (p *SAMLProvider) trackerCookie(value string, maxAge int) *http.Cookie {
	secure := p.SP.ServiceProvider.AcsURL.Scheme == "https"

	c := &http.Cookie{ //nolint:gosec // G124: Secure and HttpOnly are set; SameSite=None is applied below only when Secure (cross-site ACS POST)
		Name:     samlRequestCookieName(p.Slug),
		Value:    value,
		Path:     p.SP.ServiceProvider.AcsURL.Path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
	}

	// The ACS request is a cross-site POST from the IdP, so a Lax cookie would
	// not be sent. SameSite=None requires Secure; over plain HTTP the attribute
	// is left unset.
	if secure {
		c.SameSite = http.SameSiteNoneMode
	}

	return c
}

// StartLogin builds the AuthnRequest, records its ID in a signed, short-lived
// cookie scoped to the ACS path, and returns the IdP redirect URL. The ACS
// refuses any response that is not an answer to a request tracked this way.
func (p *SAMLProvider) StartLogin(w http.ResponseWriter, relayState string, trackerKey []byte) (string, error) {
	if len(trackerKey) == 0 {
		return "", ErrSAMLTrackerKey
	}

	sp := &p.SP.ServiceProvider

	authnReq, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", fmt.Errorf("make authn request: %w", err)
	}

	redirectURL, err := authnReq.Redirect(relayState, sp)
	if err != nil {
		return "", fmt.Errorf("make redirect authn request: %w", err)
	}

	value := signSAMLRequestID(trackerKey, p.Slug, authnReq.ID, time.Now().Add(samlRequestTTL))
	http.SetCookie(w, p.trackerCookie(value, int(samlRequestTTL.Seconds())))

	return redirectURL.String(), nil
}

// SAMLResponseMode says how an ACS request is validated.
type SAMLResponseMode int

const (
	// SAMLSPInitiated: the response must answer a request tracked by StartLogin.
	SAMLSPInitiated SAMLResponseMode = iota
	// SAMLIDPInitiated: an unsolicited response, accepted only when the provider
	// enables it.
	SAMLIDPInitiated
)

// ResponseMode routes an ACS request. It peeks at Response/@InResponseTo in
// the (still unverified) document: a response that names a request is
// SP-initiated; one that names none is IdP-initiated, which only a provider
// with allow_idp_initiated serves. The peek is a routing hint, never a
// security decision: each path validates the response in full, and the
// IdP-initiated path itself insists on an empty InResponseTo, so a document
// that lies to the peek is rejected either way.
func (p *SAMLProvider) ResponseMode(r *http.Request) SAMLResponseMode {
	if p.idpInitiatedSP == nil {
		return SAMLSPInitiated
	}

	if err := r.ParseForm(); err != nil || r.PostForm.Get("SAMLart") != "" {
		return SAMLSPInitiated
	}

	inResponseTo, ok := peekSAMLInResponseTo(r.PostForm.Get("SAMLResponse"))
	if ok && inResponseTo == "" {
		return SAMLIDPInitiated
	}

	return SAMLSPInitiated
}

// peekSAMLInResponseTo reads the InResponseTo attribute of the root
// samlp:Response element of a base64 SAMLResponse without validating anything.
// ok is false when the payload is not a decodable Response document.
func peekSAMLInResponseTo(b64 string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", false
	}

	dec := xml.NewDecoder(bytes.NewReader(raw)) //nolint:gosec // G709: stops at the root start element; body capped by net/http ParseForm (10 MiB)

	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}

		start, isStart := tok.(xml.StartElement)
		if !isStart {
			continue
		}

		if start.Name.Local != "Response" || start.Name.Space != "urn:oasis:names:tc:SAML:2.0:protocol" {
			return "", false
		}

		for _, a := range start.Attr {
			if a.Name.Local == "InResponseTo" && a.Name.Space == "" {
				return a.Value, true
			}
		}

		return "", true
	}
}

// ParseCallback validates a SAML response at the ACS and returns its verified
// assertion. See ResponseMode for the two paths.
//
// SP-initiated: the response must answer the AuthnRequest tracked by
// StartLogin (signed cookie, InResponseTo), carry a valid signature, audience
// and time window, and its assertion ID must not have been seen before. The
// tracker cookie is cleared whatever the outcome (single use).
//
// IdP-initiated (opt-in): the same validation, with InResponseTo required to be
// empty on the response and on every SubjectConfirmationData; no tracker is
// consulted and an SP-initiated tracker cookie is left alone.
func (p *SAMLProvider) ParseCallback(w http.ResponseWriter, r *http.Request, db database.Database, trackerKey []byte) (*saml.Assertion, error) {
	if len(trackerKey) == 0 {
		return nil, ErrSAMLTrackerKey
	}

	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse form: %w", err)
	}

	if p.ResponseMode(r) == SAMLIDPInitiated {
		return p.parseIDPInitiated(r, db)
	}

	cookie, err := r.Cookie(samlRequestCookieName(p.Slug))
	if err != nil {
		return nil, ErrSAMLRequestUntracked
	}

	http.SetCookie(w, p.trackerCookie("", -1))

	requestID, err := verifySAMLRequestID(trackerKey, p.Slug, cookie.Value, time.Now())
	if err != nil {
		return nil, err
	}

	assertion, err := p.SP.ServiceProvider.ParseResponse(r, []string{requestID})
	if err != nil {
		return nil, classifySAMLParseError(err)
	}

	if err := p.consumeAssertion(r.Context(), db, assertion); err != nil {
		return nil, err
	}

	return assertion, nil
}

func (p *SAMLProvider) parseIDPInitiated(r *http.Request, db database.Database) (*saml.Assertion, error) {
	assertion, err := p.idpInitiatedSP.ParseResponse(r, nil)
	if err != nil {
		return nil, classifySAMLParseError(err)
	}

	// The library skips the SubjectConfirmationData InResponseTo check for
	// IdP-initiated responses; an assertion that claims to answer a request is
	// not unsolicited, so it is refused here.
	if assertion.Subject != nil {
		for _, sc := range assertion.Subject.SubjectConfirmations {
			if sc.SubjectConfirmationData != nil && sc.SubjectConfirmationData.InResponseTo != "" {
				return nil, fmt.Errorf("%w: %w", ErrSAMLRequestMismatch, errSAMLUnsolicitedInResponseTo)
			}
		}
	}

	if err := p.consumeAssertion(r.Context(), db, assertion); err != nil {
		return nil, err
	}

	return assertion, nil
}

// classifySAMLParseError maps the library's InvalidResponseError (whose public
// message is deliberately generic) onto this package's sentinels through the
// library's typed errors. Anything it does not recognize is returned as is.
//

func classifySAMLParseError(err error) error {
	// InvalidResponseError's own message is deliberately generic; the private
	// cause carries the diagnostic text (still only for logs, never clients).
	detail := err

	var invalid *saml.InvalidResponseError
	if errors.As(err, &invalid) && invalid.PrivateErr != nil {
		detail = invalid.PrivateErr
	}

	switch {
	case errors.Is(err, saml.ErrSignatureMissing):
		return fmt.Errorf("%w: %w", ErrSAMLSignatureMissing, detail)
	case errors.Is(err, saml.ErrSignatureInvalid):
		return fmt.Errorf("%w: %w", ErrSAMLSignatureInvalid, detail)
	case errors.Is(err, saml.ErrAudienceMismatch):
		return fmt.Errorf("%w: %w", ErrSAMLAudienceMismatch, detail)
	case errors.Is(err, saml.ErrExpired):
		return fmt.Errorf("%w: %w", ErrSAMLAssertionExpired, detail)
	case errors.Is(err, saml.ErrNotYetValid):
		return fmt.Errorf("%w: %w", ErrSAMLAssertionNotYetValid, detail)
	case errors.Is(err, saml.ErrInResponseToMismatch):
		return fmt.Errorf("%w: %w", ErrSAMLRequestMismatch, detail)
	case errors.Is(err, saml.ErrDestinationMismatch), errors.Is(err, saml.ErrRecipientMismatch):
		return fmt.Errorf("%w: %w", ErrSAMLDestinationMismatch, detail)
	case errors.Is(err, saml.ErrIssuerMismatch):
		return fmt.Errorf("%w: %w", ErrSAMLIssuerMismatch, detail)
	}

	if bad := (saml.ErrBadStatus{}); errors.As(err, &bad) {
		return fmt.Errorf("%w: %w", ErrSAMLBadStatus, detail)
	}

	return err
}

// consumeAssertion records the assertion ID in the shared replay cache for the
// assertion's remaining validity; a second presentation is rejected.
func (p *SAMLProvider) consumeAssertion(ctx context.Context, db database.Database, a *saml.Assertion) error {
	if a.ID == "" {
		return fmt.Errorf("%w: assertion has no ID", ErrSAMLReplay)
	}

	expiresAt := samlAssertionExpiry(a, time.Now())

	first, err := db.ConsumeSAMLAssertion(ctx, p.Slug, a.ID, expiresAt)
	if err != nil {
		return fmt.Errorf("record saml assertion: %w", err)
	}

	if !first {
		return ErrSAMLReplay
	}

	return nil
}

// samlAssertionExpiry returns how long an assertion ID must stay in the replay
// cache: the latest NotOnOrAfter the assertion carries plus clock skew and a
// safety margin, or a default window when it carries none.
func samlAssertionExpiry(a *saml.Assertion, now time.Time) time.Time {
	var latest time.Time

	if a.Conditions != nil && a.Conditions.NotOnOrAfter.After(latest) {
		latest = a.Conditions.NotOnOrAfter
	}

	if a.Subject != nil {
		for _, sc := range a.Subject.SubjectConfirmations {
			if sc.SubjectConfirmationData != nil && sc.SubjectConfirmationData.NotOnOrAfter.After(latest) {
				latest = sc.SubjectConfirmationData.NotOnOrAfter
			}
		}
	}

	if latest.IsZero() {
		latest = now.Add(samlRequestTTL)
	}

	return latest.Add(saml.MaxClockSkew + samlReplayRetention)
}

func (p *SAMLProvider) MetadataXML() ([]byte, error) {
	metadata := p.SP.ServiceProvider.Metadata()

	buf, err := xml.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sp metadata: %w", err)
	}

	out := append([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"), buf...)

	return out, nil
}
