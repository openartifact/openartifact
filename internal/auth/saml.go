// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
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
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
)

var (
	ErrIDPMetadataUnavailable = errors.New("idp metadata unavailable")
	ErrSAMLSignatureInvalid   = errors.New("saml signature invalid")
	ErrSAMLAudienceMismatch   = errors.New("saml audience mismatch")
	ErrSAMLAssertionExpired   = errors.New("saml assertion expired")
)

//nolint:gochecknoglobals // process-lifetime SAML provider cache, keyed by slug
var samlCache sync.Map

//nolint:gochecknoglobals // process-lifetime SAML provider cache
var samlMetadataCache sync.Map

//nolint:gochecknoglobals // process-lifetime ephemeral SP keypair (used only when none is configured)
var (
	ephemeralSAMLMu   sync.Mutex
	ephemeralSAMLKey  *rsa.PrivateKey
	ephemeralSAMLCert *x509.Certificate
)

type cachedMetadata struct {
	descriptor *saml.EntityDescriptor
	fetchedAt  time.Time
}

type SAMLProvider struct {
	Slug     string
	Cfg      models.SAMLConfig
	SP       *samlsp.Middleware
	EntityID string
	AcsURL   string

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

	return LoadSAMLProviderRow(ctx, row, baseURL, appCfg, log)
}

// LoadSAMLProviderRow builds the provider for an already-fetched row without
// checking Enabled; it exists so admins can test-connect a disabled provider.
// Public login paths MUST use LoadSAMLProvider.
func LoadSAMLProviderRow(ctx context.Context, row *models.SSOProvider, baseURL string, appCfg *config.Config, log *logger.Logger) (*SAMLProvider, error) {
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

	idpMeta, err := fetchIDPMetadata(ctx, samlCfg, log)
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

	rsaKey, cert, err := loadOrGenerateSAMLKeypair(appCfg, log)
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

	opts := samlsp.Options{
		EntityID:          entityID,
		URL:               *acsURLParsed,
		Key:               rsaKey,
		Certificate:       cert,
		IDPMetadata:       idpMeta,
		AllowIDPInitiated: false,
	}

	sp, err := samlsp.New(opts)
	if err != nil {
		return nil, fmt.Errorf("create saml middleware: %w", err)
	}

	sp.ServiceProvider.AcsURL = *acsURLParsed
	sp.ServiceProvider.MetadataURL = *metadataURL

	p := &SAMLProvider{
		Slug:        slug,
		Cfg:         samlCfg,
		SP:          sp,
		EntityID:    entityID,
		AcsURL:      acsURL,
		fingerprint: fingerprint,
	}

	samlCache.Store(slug, p)

	return p, nil
}

// samlFingerprint hashes everything a cached provider is derived from: the row
// version, the (freshly fetched) IdP metadata, the attribute mapping and the SP
// entity ID. Any change yields a different fingerprint, so a rotated or revoked
// IdP certificate or an edited configuration is never served stale.
func samlFingerprint(row *models.SSOProvider, idpMeta *saml.EntityDescriptor, entityID string) (string, error) {
	metaXML, err := xml.Marshal(idpMeta)
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

func generateSelfSignedCert() (*rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "openartifact-saml-sp"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}

	return key, cert, nil
}

func loadOrGenerateSAMLKeypair(cfg *config.Config, log *logger.Logger) (*rsa.PrivateKey, *x509.Certificate, error) {
	// If both cert and key are configured via environment variables, load them
	if cfg.Auth.SAMLSPCertPEM != "" && cfg.Auth.SAMLSPKeyPEM != "" { //nolint:nestif // SAML cert loading requires nested validation
		_, err := tls.X509KeyPair([]byte(cfg.Auth.SAMLSPCertPEM), []byte(cfg.Auth.SAMLSPKeyPEM))
		if err != nil {
			return nil, nil, fmt.Errorf("parse saml sp keypair from config: %w", err)
		}

		// Parse the private key
		keyBlock, _ := pem.Decode([]byte(cfg.Auth.SAMLSPKeyPEM))
		if keyBlock == nil {
			return nil, nil, errors.New("failed to decode SAML SP private key PEM") //nolint:err113 // static error
		}

		privKey, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			// Try PKCS8 format
			parsedKey, err2 := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
			if err2 != nil {
				return nil, nil, fmt.Errorf("parse saml sp private key: %w", err)
			}

			var ok bool

			privKey, ok = parsedKey.(*rsa.PrivateKey)
			if !ok {
				return nil, nil, errors.New("saml sp private key is not an RSA key") //nolint:err113 // static error
			}
		}

		// Parse the certificate
		certBlock, _ := pem.Decode([]byte(cfg.Auth.SAMLSPCertPEM))
		if certBlock == nil {
			return nil, nil, errors.New("failed to decode SAML SP certificate PEM") //nolint:err113 // static error
		}

		cert, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse saml sp certificate: %w", err)
		}

		if log != nil {
			log.Infow("Loaded SAML SP keypair from environment variables", "cert_subject", cert.Subject.String())
		}

		return privKey, cert, nil
	}

	// One ephemeral keypair per process, so rebuilding a provider (config or
	// IdP metadata change) does not change the SP certificate mid-flight.
	ephemeralSAMLMu.Lock()
	defer ephemeralSAMLMu.Unlock()

	if ephemeralSAMLKey != nil {
		return ephemeralSAMLKey, ephemeralSAMLCert, nil
	}

	if log != nil {
		log.Warnw("SAML SP keypair not configured via environment variables; using ephemeral keypair. " +
			"Set OPENARTIFACT_SAML_SP_CERT and OPENARTIFACT_SAML_SP_KEY for persistence across restarts. " +
			"Generate with: openssl req -x509 -newkey rsa:2048 -keyout sp.key -out sp.crt -days 3650 -nodes -subj \"/CN=openartifact-sp\"")
	}

	key, cert, err := generateSelfSignedCert()
	if err != nil {
		return nil, nil, err
	}

	ephemeralSAMLKey, ephemeralSAMLCert = key, cert

	return key, cert, nil
}

func fetchIDPMetadata(ctx context.Context, cfg models.SAMLConfig, log *logger.Logger) (*saml.EntityDescriptor, error) {
	if cfg.IDPMetadataURL != "" {
		return fetchIDPMetadataFromURL(ctx, cfg.IDPMetadataURL, log)
	}

	if cfg.IDPMetadataXML != "" {
		cacheKey := "static:" + cfg.IDPMetadataXML
		if cached, ok := samlMetadataCache.Load(cacheKey); ok {
			if cm, ok := cached.(*cachedMetadata); ok {
				return cm.descriptor, nil
			}
		}

		meta, err := samlsp.ParseMetadata([]byte(cfg.IDPMetadataXML))
		if err != nil {
			return nil, fmt.Errorf("parse idp metadata xml: %w", err)
		}

		samlMetadataCache.Store(cacheKey, &cachedMetadata{descriptor: meta, fetchedAt: time.Now()})

		return meta, nil
	}

	return nil, errors.New("no idp metadata source configured") //nolint:err113 // static error
}

const (
	maxIDPMetadataBytes = 1 << 20
	maxStaleIDPMetadata = 24 * time.Hour
)

var errIDPMetadataTooLarge = errors.New("idp metadata exceeds size limit")

func fetchIDPMetadataFromURL(ctx context.Context, metadataURL string, log *logger.Logger) (*saml.EntityDescriptor, error) {
	const cacheTTL = time.Hour

	if cached, ok := samlMetadataCache.Load(metadataURL); ok {
		if entry, ok := cached.(*cachedMetadata); ok {
			if time.Since(entry.fetchedAt) < cacheTTL {
				return entry.descriptor, nil
			}
		}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// The URL is admin-supplied: reject private/loopback targets (unless
	// OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true) and re-check at dial time.
	if err := netutil.ValidateOutboundURLContext(fetchCtx, metadataURL); err != nil {
		return fallbackCachedMetadata(metadataURL, fmt.Errorf("idp metadata url rejected: %w", err), log)
	}

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return fallbackCachedMetadata(metadataURL, fmt.Errorf("build metadata request: %w", err), log)
	}

	resp, err := telemetry.WrapClient(netutil.SafeHTTPClient(10 * time.Second)).Do(req)
	if err != nil {
		return fallbackCachedMetadata(metadataURL, err, log)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIDPMetadataBytes+1))
	if err != nil {
		return fallbackCachedMetadata(metadataURL, err, log)
	}

	if len(body) > maxIDPMetadataBytes {
		return fallbackCachedMetadata(metadataURL, errIDPMetadataTooLarge, log)
	}

	meta, err := samlsp.ParseMetadata(body)
	if err != nil {
		return fallbackCachedMetadata(metadataURL, err, log)
	}

	samlMetadataCache.Store(metadataURL, &cachedMetadata{descriptor: meta, fetchedAt: time.Now()})

	return meta, nil
}

func fallbackCachedMetadata(metadataURL string, fetchErr error, log *logger.Logger) (*saml.EntityDescriptor, error) {
	if cached, ok := samlMetadataCache.Load(metadataURL); ok {
		if log != nil {
			log.Warnw("SAML IdP metadata URL unreachable; using cached copy", "url", metadataURL, "error", fetchErr)
		}

		// A stale copy is served only for a bounded time, so a revoked IdP
		// certificate is eventually dropped when the URL stays unreachable.
		if cm, ok := cached.(*cachedMetadata); ok && time.Since(cm.fetchedAt) < maxStaleIDPMetadata {
			return cm.descriptor, nil
		}
	}

	return nil, fmt.Errorf("%w: %w", ErrIDPMetadataUnavailable, fetchErr)
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

// ParseCallback validates a SAML response at the ACS: it must answer the
// AuthnRequest tracked by StartLogin (InResponseTo), carry a valid signature,
// audience and time window, and its assertion ID must not have been seen
// before. The tracker cookie is cleared whatever the outcome (single use).
// Unsolicited (IdP-initiated) responses are rejected.
func (p *SAMLProvider) ParseCallback(w http.ResponseWriter, r *http.Request, db database.Database, trackerKey []byte) (*saml.Assertion, error) {
	if len(trackerKey) == 0 {
		return nil, ErrSAMLTrackerKey
	}

	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse form: %w", err)
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

// classifySAMLParseError maps crewjam's opaque InvalidResponseError (whose
// public message is deliberately generic) onto this package's sentinels using
// the private cause.
func classifySAMLParseError(err error) error {
	detail := err
	if ire := (*saml.InvalidResponseError)(nil); errors.As(err, &ire) && ire.PrivateErr != nil {
		detail = ire.PrivateErr
	}

	msg := strings.ToLower(detail.Error())

	switch {
	case strings.Contains(msg, "signature"):
		return fmt.Errorf("%w: %w", ErrSAMLSignatureInvalid, err)
	case strings.Contains(msg, "audience"):
		return fmt.Errorf("%w: %w", ErrSAMLAudienceMismatch, err)
	case strings.Contains(msg, "expired") || strings.Contains(msg, "notonorafter"):
		return fmt.Errorf("%w: %w", ErrSAMLAssertionExpired, err)
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
