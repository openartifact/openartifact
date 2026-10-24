// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// System-setting keys. The images live in system_settings next to the text
// values (base64 inside JSON): they are public, capped at 512 KiB, needed by
// the login page before any other subsystem, travel with the database backup
// and have no lifecycle to garbage-collect (no refcount, no blob store round
// trip on every page view).
const (
	settingsKey    = "branding.settings"
	imageKeyPrefix = "branding.image."

	// cacheTTL bounds how long another replica can show stale branding after
	// an administrator saved a change; the replica that handled the change
	// refreshes immediately.
	cacheTTL = 10 * time.Second
	// errorTTL is how long a snapshot built while the database was unreachable
	// is reused before retrying.
	errorTTL = 3 * time.Second
)

// SettingStore reads and writes system settings (database.Database satisfies it).
// errNoStore is returned by mutations on a manager without a settings store.
var errNoStore = errors.New("branding: no settings store")

type SettingStore interface {
	GetSystemSetting(ctx context.Context, key string) (string, error)
	SetSystemSetting(ctx context.Context, key, value string) error
}

// Public is the effective branding served to everyone (GET /api/v1/branding).
type Public struct {
	ProductName  string `json:"product_name"`
	PrimaryColor string `json:"primary_color"`
	Theme        *Theme `json:"theme"`
	LogoLightURL string `json:"logo_light_url"`
	LogoDarkURL  string `json:"logo_dark_url"`
	FaviconURL   string `json:"favicon_url"`
	LoginMessage string `json:"login_message"`
	SupportURL   string `json:"support_url"`
	SourceURL    string `json:"source_url"`
	Revision     string `json:"revision"`
}

// DefaultsView is the configured baseline reported to the admin UI.
type DefaultsView struct {
	ProductName  string `json:"product_name"`
	PrimaryColor string `json:"primary_color"`
	LoginMessage string `json:"login_message"`
	SupportURL   string `json:"support_url"`
	SourceURL    string `json:"source_url"`
}

// Overrides reports which values come from a runtime override.
type Overrides struct {
	ProductName  bool `json:"product_name"`
	PrimaryColor bool `json:"primary_color"`
	LoginMessage bool `json:"login_message"`
	SupportURL   bool `json:"support_url"`
	SourceURL    bool `json:"source_url"`
	LogoLight    bool `json:"logo_light"`
	LogoDark     bool `json:"logo_dark"`
	Favicon      bool `json:"favicon"`
}

// Admin is the admin view (GET/PUT /api/v1/admin/branding).
type Admin struct {
	Effective Public       `json:"effective"`
	Defaults  DefaultsView `json:"defaults"`
	Overrides Overrides    `json:"overrides"`
}

// Snapshot is one consistent, immutable view of the effective branding. It is
// rebuilt at most every cacheTTL and after every write.
type Snapshot struct {
	Public Public
	Admin  Admin
	// JSON is the encoded Public and ETag its validator (quoted).
	JSON []byte
	ETag string
	// Images are the effective images by kind.
	Images map[Kind]*Image

	expires time.Time
}

// Image returns the effective image of kind, if any.
func (s *Snapshot) Image(k Kind) (*Image, bool) {
	img, ok := s.Images[k]

	return img, ok
}

// stored is the runtime override document; a nil field is not overridden.
type stored struct {
	ProductName  *string `json:"product_name,omitempty"`
	PrimaryColor *string `json:"primary_color,omitempty"`
	LoginMessage *string `json:"login_message,omitempty"`
	SupportURL   *string `json:"support_url,omitempty"`
	SourceURL    *string `json:"source_url,omitempty"`
}

type storedImage struct {
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}

// OptString is a JSON field that distinguishes absent (leave alone), null
// (remove the override) and a string (set the override).
type OptString struct {
	Set   bool
	Value *string
}

// UnmarshalJSON implements json.Unmarshaler.
func (o *OptString) UnmarshalJSON(b []byte) error {
	o.Set = true

	if string(b) == "null" {
		o.Value = nil
		return nil
	}

	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("expected a string or null: %w", err)
	}

	o.Value = &s

	return nil
}

// Update is the body of PUT /api/v1/admin/branding.
type Update struct {
	ProductName  OptString `json:"product_name"`
	PrimaryColor OptString `json:"primary_color"`
	LoginMessage OptString `json:"login_message"`
	SupportURL   OptString `json:"support_url"`
	SourceURL    OptString `json:"source_url"`
}

// Logger is the subset of the application logger the manager uses.
type Logger interface {
	Warnw(msg string, keysAndValues ...any)
}

// Manager layers the administrator's runtime overrides (system settings) over
// the configured defaults and caches the result.
type Manager struct {
	store SettingStore
	def   Defaults
	log   Logger

	mu   sync.Mutex // serialises writes and refreshes
	snap atomic.Pointer[Snapshot]
}

// NewManager returns a manager over store; a nil store serves the defaults only.
func NewManager(store SettingStore, def Defaults, log Logger) *Manager {
	if def.ProductName == "" {
		def.ProductName = DefaultProductName
	}

	if def.SourceURL == "" {
		def.SourceURL = DefaultSourceURL
	}

	return &Manager{store: store, def: def, log: log}
}

// Snapshot returns the current effective branding. It never fails: when the
// database is unreachable the last good snapshot (else the configured
// defaults) is served and the read is retried shortly.
func (m *Manager) Snapshot(ctx context.Context) *Snapshot {
	if s := m.snap.Load(); s != nil && time.Now().Before(s.expires) {
		return s
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if s := m.snap.Load(); s != nil && time.Now().Before(s.expires) {
		return s
	}

	return m.refreshLocked(ctx)
}

func (m *Manager) refreshLocked(ctx context.Context) *Snapshot {
	st, imgs, err := m.load(ctx)
	if err != nil {
		if m.log != nil {
			m.log.Warnw("branding: reading runtime settings failed, serving the last known branding", "error", err)
		}

		if prev := m.snap.Load(); prev != nil {
			cp := *prev
			cp.expires = time.Now().Add(errorTTL)
			m.snap.Store(&cp)

			return &cp
		}

		s := m.build(stored{}, nil)
		s.expires = time.Now().Add(errorTTL)
		m.snap.Store(s)

		return s
	}

	s := m.build(st, imgs)
	s.expires = time.Now().Add(cacheTTL)
	m.snap.Store(s)

	return s
}

func (m *Manager) load(ctx context.Context) (stored, map[Kind]*Image, error) {
	var st stored

	if m.store == nil {
		return st, nil, nil
	}

	raw, err := m.store.GetSystemSetting(ctx, settingsKey)
	if err != nil {
		return st, nil, fmt.Errorf("get %s: %w", settingsKey, err)
	}

	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			// A malformed document is treated as "no overrides" rather than
			// taking the login page down; an administrator can save again.
			st = stored{}
		}
	}

	imgs := make(map[Kind]*Image)

	for _, k := range Kinds() {
		raw, err := m.store.GetSystemSetting(ctx, imageKeyPrefix+string(k))
		if err != nil {
			return st, nil, fmt.Errorf("get image %s: %w", k, err)
		}

		if raw == "" {
			continue
		}

		var si storedImage
		if json.Unmarshal([]byte(raw), &si) != nil {
			continue
		}

		if img, ok := restoreImage(si.ContentType, si.Data); ok {
			imgs[k] = img
		}
	}

	return st, imgs, nil
}

func pick(override *string, def string) string {
	if override != nil {
		return *override
	}

	return def
}

func (m *Manager) build(st stored, overrideImages map[Kind]*Image) *Snapshot {
	pub := Public{
		ProductName:  pick(st.ProductName, m.def.ProductName),
		PrimaryColor: pick(st.PrimaryColor, m.def.PrimaryColor),
		LoginMessage: pick(st.LoginMessage, m.def.LoginMessage),
		SupportURL:   pick(st.SupportURL, m.def.SupportURL),
		SourceURL:    pick(st.SourceURL, m.def.SourceURL),
	}

	images := make(map[Kind]*Image)

	for _, k := range Kinds() {
		if img, ok := overrideImages[k]; ok {
			images[k] = img
		} else if img, ok := m.def.Images[k]; ok {
			images[k] = img
		}
	}

	if theme, ok := DeriveTheme(pub.PrimaryColor); ok {
		pub.Theme = &theme
	}

	pub.Revision = revision(pub, images)
	pub.LogoLightURL = logoURL(images, LogoLight, LogoDark, pub.Revision)
	pub.LogoDarkURL = logoURL(images, LogoDark, LogoLight, pub.Revision)

	if _, ok := images[Favicon]; ok {
		pub.FaviconURL = imageURL(Favicon, pub.Revision)
	}

	body, _ := json.Marshal(pub) //nolint:errchkjson // plain strings and structs cannot fail to encode
	sum := sha256.Sum256(body)

	return &Snapshot{
		Public: pub,
		Admin: Admin{
			Effective: pub,
			Defaults: DefaultsView{
				ProductName: m.def.ProductName, PrimaryColor: m.def.PrimaryColor,
				LoginMessage: m.def.LoginMessage, SupportURL: m.def.SupportURL, SourceURL: m.def.SourceURL,
			},
			Overrides: Overrides{
				ProductName: st.ProductName != nil, PrimaryColor: st.PrimaryColor != nil,
				LoginMessage: st.LoginMessage != nil, SupportURL: st.SupportURL != nil, SourceURL: st.SourceURL != nil,
				LogoLight: overrideImages[LogoLight] != nil, LogoDark: overrideImages[LogoDark] != nil,
				Favicon: overrideImages[Favicon] != nil,
			},
		},
		JSON:   body,
		ETag:   `"` + hex.EncodeToString(sum[:12]) + `"`,
		Images: images,
	}
}

func imageURL(k Kind, rev string) string {
	return "/api/v1/branding/" + string(k) + "?v=" + rev
}

// logoURL points at the image of kind, or at the other logo when only that one
// is set; empty means the built-in lockup.
func logoURL(images map[Kind]*Image, kind, other Kind, rev string) string {
	if _, ok := images[kind]; ok {
		return imageURL(kind, rev)
	}

	if _, ok := images[other]; ok {
		return imageURL(other, rev)
	}

	return ""
}

func revision(p Public, images map[Kind]*Image) string {
	h := sha256.New()

	for _, s := range []string{p.ProductName, p.PrimaryColor, p.LoginMessage, p.SupportURL, p.SourceURL} {
		fmt.Fprintf(h, "%d:%s|", len(s), s)
	}

	for _, k := range Kinds() {
		if img, ok := images[k]; ok {
			fmt.Fprintf(h, "%s=%s|", k, img.SHA256)
		}
	}

	return hex.EncodeToString(h.Sum(nil)[:6])
}

// Update applies a partial update of the text overrides and returns the new
// snapshot. Every provided value is validated before anything is written.
func (m *Manager) Update(ctx context.Context, u Update) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		return nil, errNoStore
	}

	st, _, err := m.load(ctx)
	if err != nil {
		return nil, err
	}

	fields := []struct {
		in       OptString
		dst      **string
		validate func(string) (string, error)
		name     string
	}{
		{u.ProductName, &st.ProductName, ValidateProductName, "product_name"},
		{u.PrimaryColor, &st.PrimaryColor, ValidateColor, "primary_color"},
		{u.LoginMessage, &st.LoginMessage, ValidateLoginMessage, "login_message"},
		{u.SupportURL, &st.SupportURL, ValidateSupportURL, "support_url"},
		{u.SourceURL, &st.SourceURL, validateSourceOverride, "source_url"},
	}

	for _, f := range fields {
		if !f.in.Set {
			continue
		}

		if f.in.Value == nil {
			*f.dst = nil
			continue
		}

		v, err := f.validate(*f.in.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}

		*f.dst = &v
	}

	if err := m.writeSettings(ctx, st); err != nil {
		return nil, err
	}

	return m.refreshLocked(ctx), nil
}

// validateSourceOverride is ValidateSourceURL without the "empty means
// default" shortcut: an explicit override cannot be empty.
func validateSourceOverride(s string) (string, error) {
	if s == "" {
		return "", invalidf("source URL must not be empty (send null to restore the default)")
	}

	return ValidateSourceURL(s)
}

func (m *Manager) writeSettings(ctx context.Context, st stored) error {
	value := ""

	if st != (stored{}) {
		b, err := json.Marshal(st)
		if err != nil {
			return fmt.Errorf("encode branding settings: %w", err)
		}

		value = string(b)
	}

	if err := m.store.SetSystemSetting(ctx, settingsKey, value); err != nil {
		return fmt.Errorf("save branding settings: %w", err)
	}

	return nil
}

// SetImage stores a runtime image override.
func (m *Manager) SetImage(ctx context.Context, k Kind, img *Image) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		return nil, errNoStore
	}

	b, err := json.Marshal(storedImage{ContentType: img.ContentType, Data: img.Data})
	if err != nil {
		return nil, fmt.Errorf("encode branding image: %w", err)
	}

	if err := m.store.SetSystemSetting(ctx, imageKeyPrefix+string(k), string(b)); err != nil {
		return nil, fmt.Errorf("save branding image: %w", err)
	}

	return m.refreshLocked(ctx), nil
}

// ClearImage removes the runtime override of an image (back to the configured
// file, else the built-in one).
func (m *Manager) ClearImage(ctx context.Context, k Kind) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		return nil, errNoStore
	}

	if err := m.store.SetSystemSetting(ctx, imageKeyPrefix+string(k), ""); err != nil {
		return nil, fmt.Errorf("remove branding image: %w", err)
	}

	return m.refreshLocked(ctx), nil
}

// Reset removes every runtime override: text settings and all images.
func (m *Manager) Reset(ctx context.Context) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		return nil, errNoStore
	}

	if err := m.writeSettings(ctx, stored{}); err != nil {
		return nil, err
	}

	for _, k := range Kinds() {
		if err := m.store.SetSystemSetting(ctx, imageKeyPrefix+string(k), ""); err != nil {
			return nil, fmt.Errorf("remove branding image: %w", err)
		}
	}

	return m.refreshLocked(ctx), nil
}
