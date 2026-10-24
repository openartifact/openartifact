// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu   sync.Mutex
	m    map[string]string
	fail bool
}

func newMemStore() *memStore { return &memStore{m: map[string]string{}} }

func (s *memStore) GetSystemSetting(_ context.Context, k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fail {
		return "", errors.New("db down")
	}

	return s.m[k], nil
}

func (s *memStore) SetSystemSetting(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.m[k] = v

	return nil
}

func str(s string) OptString { return OptString{Set: true, Value: &s} }

func nullOpt() OptString { return OptString{Set: true} }

func TestManager_DefaultsAndOverrideLayering(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	m := NewManager(st, Defaults{ProductName: "FromConfig", PrimaryColor: "#0f766e", SupportURL: "https://help.example.com"}, nil)

	snap := m.Snapshot(ctx)
	if snap.Public.ProductName != "FromConfig" || snap.Public.SourceURL != DefaultSourceURL || snap.Public.Theme == nil {
		t.Fatalf("defaults = %+v", snap.Public)
	}

	if snap.Admin.Overrides != (Overrides{}) {
		t.Errorf("overrides = %+v, want none", snap.Admin.Overrides)
	}

	// Runtime overrides win over the configuration; untouched fields keep the default.
	snap, err := m.Update(ctx, Update{ProductName: str("  Acme  "), PrimaryColor: str("#FF0000"), LoginMessage: str("Hi **there**")})
	if err != nil {
		t.Fatal(err)
	}

	p := snap.Public
	if p.ProductName != "Acme" || p.PrimaryColor != "#ff0000" || p.LoginMessage != "Hi **there**" || p.SupportURL != "https://help.example.com" {
		t.Fatalf("effective = %+v", p)
	}

	if o := snap.Admin.Overrides; !o.ProductName || !o.PrimaryColor || !o.LoginMessage || o.SupportURL || o.SourceURL {
		t.Errorf("overrides = %+v", o)
	}

	if snap.Admin.Defaults.ProductName != "FromConfig" {
		t.Errorf("defaults view = %+v", snap.Admin.Defaults)
	}
}

func TestManager_BlankNullReplicaAndReset(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	defaults := Defaults{ProductName: "FromConfig", PrimaryColor: "#0f766e", SupportURL: "https://help.example.com"}
	m := NewManager(st, defaults, nil)

	if _, err := m.Update(ctx, Update{ProductName: str("Acme"), PrimaryColor: str("#ff0000")}); err != nil {
		t.Fatal(err)
	}

	// An empty string is a valid override for message and support link (it blanks the configured value).
	snap, err := m.Update(ctx, Update{SupportURL: str("")})
	if err != nil || snap.Public.SupportURL != "" || !snap.Admin.Overrides.SupportURL || snap.Public.ProductName != "Acme" {
		t.Fatalf("blanked support: %+v, %v", snap.Public, err)
	}

	// null removes one override only.
	snap, err = m.Update(ctx, Update{ProductName: nullOpt()})
	if err != nil || snap.Public.ProductName != "FromConfig" || snap.Public.PrimaryColor != "#ff0000" {
		t.Fatalf("null reset: %+v, %v", snap.Public, err)
	}

	// Another manager over the same store (another replica) sees the saved state.
	other := NewManager(st, defaults, nil)
	if got := other.Snapshot(ctx).Public; got.PrimaryColor != "#ff0000" || got.Revision != snap.Public.Revision {
		t.Errorf("replica sees %+v", got)
	}

	snap, err = m.Reset(ctx)
	if err != nil || snap.Public.ProductName != "FromConfig" || snap.Public.PrimaryColor != "#0f766e" || snap.Admin.Overrides != (Overrides{}) {
		t.Fatalf("reset: %+v, %v", snap.Public, err)
	}
}

func TestManager_UpdateValidatesBeforeWriting(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	m := NewManager(st, Defaults{}, nil)

	for name, u := range map[string]Update{
		"empty name":       {ProductName: str("")},
		"bad colour":       {PrimaryColor: str("blue")},
		"bad support":      {SupportURL: str("javascript:alert(1)")},
		"empty source":     {SourceURL: str("")},
		"bad source":       {SourceURL: str("ftp://x")},
		"long message":     {LoginMessage: str(strings.Repeat("x", 3000))},
		"one bad of three": {ProductName: str("Fine"), PrimaryColor: str("#12"), LoginMessage: str("ok")},
	} {
		if _, err := m.Update(ctx, u); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}

	if len(st.m) != 0 {
		t.Errorf("invalid updates wrote to the store: %v", st.m)
	}
}

func TestManager_Images(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()

	cfgImg, err := NewImage("", []byte(cleanSVG))
	if err != nil {
		t.Fatal(err)
	}

	m := NewManager(st, Defaults{Images: map[Kind]*Image{LogoLight: cfgImg}}, nil)

	snap := m.Snapshot(ctx)
	// Only one logo configured: it serves both themes; no favicon.
	if !strings.HasPrefix(snap.Public.LogoLightURL, "/api/v1/branding/logo-light?v=") ||
		!strings.HasPrefix(snap.Public.LogoDarkURL, "/api/v1/branding/logo-light?v=") || snap.Public.FaviconURL != "" {
		t.Fatalf("urls = %+v", snap.Public)
	}

	up, err := NewImage("image/png", testPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}

	before := snap.Public.Revision

	snap, err = m.SetImage(ctx, Favicon, up)
	if err != nil || snap.Public.FaviconURL == "" || !snap.Admin.Overrides.Favicon || snap.Public.Revision == before {
		t.Fatalf("favicon: %+v, %v", snap.Public, err)
	}

	if img, ok := snap.Image(Favicon); !ok || img.SHA256 != up.SHA256 {
		t.Error("favicon not served")
	}

	// A runtime dark logo overrides, and the light config logo stays.
	snap, err = m.SetImage(ctx, LogoDark, up)
	if err != nil || !strings.Contains(snap.Public.LogoDarkURL, "logo-dark") || !strings.Contains(snap.Public.LogoLightURL, "logo-light") {
		t.Fatalf("logos: %+v, %v", snap.Public, err)
	}

	// Removing the override falls back to the configured file.
	snap, err = m.ClearImage(ctx, LogoDark)
	if err != nil || !strings.Contains(snap.Public.LogoDarkURL, "logo-light") || snap.Admin.Overrides.LogoDark {
		t.Fatalf("clear: %+v, %v", snap.Public, err)
	}

	// A stored blob that is not an accepted image is ignored.
	st.m[imageKeyPrefix+"favicon"] = `{"content_type":"text/html","data":"PGh0bWw+"}`
	st.m[imageKeyPrefix+"logo-dark"] = `not json`

	if snap, err = m.Reset(ctx); err != nil {
		t.Fatal(err)
	}

	st.m[imageKeyPrefix+"favicon"] = `{"content_type":"text/html","data":"PGh0bWw+"}`
	m.snap.Store(nil)

	if _, ok := m.Snapshot(ctx).Image(Favicon); ok {
		t.Error("stored non-image was served")
	}

	_ = snap
}

func TestManager_StoreFailureServesLastGood(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	m := NewManager(st, Defaults{ProductName: "Base"}, nil)

	if _, err := m.Update(ctx, Update{ProductName: str("Saved")}); err != nil {
		t.Fatal(err)
	}

	st.fail = true
	m.snap.Store(&Snapshot{}) // expired
	m.snap.Store(nil)

	// No snapshot and a failing store: the configured defaults, never an error.
	if got := m.Snapshot(ctx).Public.ProductName; got != "Base" {
		t.Errorf("cold start with db down = %q, want Base", got)
	}

	st.fail = false
	m.snap.Store(nil)

	if got := m.Snapshot(ctx).Public.ProductName; got != "Saved" {
		t.Fatalf("after recovery = %q", got)
	}

	// With a good snapshot cached, a failing refresh keeps serving it.
	s := m.snap.Load()
	cp := *s
	cp.expires = time.Now().Add(-time.Second)
	m.snap.Store(&cp)

	st.fail = true

	if got := m.Snapshot(ctx).Public.ProductName; got != "Saved" {
		t.Errorf("refresh failure = %q, want last good Saved", got)
	}
}

func TestManager_ETagAndJSON(t *testing.T) {
	ctx := context.Background()
	m := NewManager(newMemStore(), Defaults{}, nil)

	a := m.Snapshot(ctx)

	var pub Public
	if err := json.Unmarshal(a.JSON, &pub); err != nil || pub != a.Public && pub.Revision != a.Public.Revision {
		t.Fatalf("JSON does not round-trip: %v", err)
	}

	if !strings.HasPrefix(a.ETag, `"`) || !strings.HasSuffix(a.ETag, `"`) {
		t.Errorf("ETag %q is not a quoted entity-tag", a.ETag)
	}

	// theme is serialised as an explicit null without a custom colour.
	if !strings.Contains(string(a.JSON), `"theme":null`) {
		t.Errorf("JSON = %s", a.JSON)
	}

	b, err := m.Update(ctx, Update{ProductName: str("Other")})
	if err != nil || b.ETag == a.ETag {
		t.Errorf("ETag did not change with the content: %v", err)
	}

	c, err := m.Update(ctx, Update{ProductName: str("Other")})
	if err != nil || c.ETag != b.ETag {
		t.Errorf("ETag changed without a content change")
	}
}

func TestOptString_JSON(t *testing.T) {
	var u Update
	if err := json.Unmarshal([]byte(`{"product_name":"X","primary_color":null}`), &u); err != nil {
		t.Fatal(err)
	}

	if !u.ProductName.Set || *u.ProductName.Value != "X" || !u.PrimaryColor.Set || u.PrimaryColor.Value != nil || u.LoginMessage.Set {
		t.Errorf("update = %+v", u)
	}

	if err := json.Unmarshal([]byte(`{"product_name":5}`), &u); err == nil {
		t.Error("non-string accepted")
	}
}
