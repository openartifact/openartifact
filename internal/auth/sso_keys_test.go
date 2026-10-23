// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"
)

type keySettingsStore struct{ m map[string]string }

func (s *keySettingsStore) GetSystemSetting(_ context.Context, key string) (string, error) {
	return s.m[key], nil
}

func (s *keySettingsStore) CreateSystemSettingIfAbsent(_ context.Context, key, value string) (bool, error) {
	if _, ok := s.m[key]; ok {
		return false, nil
	}

	s.m[key] = value

	return true, nil
}

// Two replicas starting without AUTH_HMAC_KEY must converge on one key; the
// environment key always wins.
func TestResolveHMACKey_SharedAcrossReplicas(t *testing.T) {
	store := &keySettingsStore{m: map[string]string{}}

	a, err := ResolveHMACKey(context.Background(), store, "", nil)
	if err != nil || len(a) == 0 {
		t.Fatalf("first replica: key=%q err=%v", a, err)
	}

	b, err := ResolveHMACKey(context.Background(), store, "", nil)
	if err != nil {
		t.Fatalf("second replica: %v", err)
	}

	if string(a) != string(b) {
		t.Fatalf("replicas diverged: %q vs %q", a, b)
	}

	env, err := ResolveHMACKey(context.Background(), store, "from-env", nil)
	if err != nil || string(env) != "from-env" {
		t.Fatalf("configured key must win: key=%q err=%v", env, err)
	}
}

func TestResolveHMACKey_NoStore(t *testing.T) {
	if _, err := ResolveHMACKey(context.Background(), nil, "", nil); err == nil {
		t.Fatal("expected an error without a store or a configured key")
	}
}
