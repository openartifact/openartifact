// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"testing"
)

var errSettingStore = errors.New("store down")

type memSettings struct {
	values   map[string]string
	getErr   error
	setErr   error
	setCalls int
}

func (m *memSettings) GetSystemSetting(_ context.Context, key string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}

	return m.values[key], nil
}

func (m *memSettings) SetSystemSetting(_ context.Context, key, value string) error {
	m.setCalls++

	if m.setErr != nil {
		return m.setErr
	}

	if m.values == nil {
		m.values = map[string]string{}
	}

	m.values[key] = value

	return nil
}

func TestProjectSettingsSelfCreate(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		def    bool
		want   bool
	}{
		{"unset uses default off", "", false, false},
		{"unset uses default on", "", true, true},
		{"stored true overrides default off", "true", false, true},
		{"stored false overrides default on", "false", true, false},
		{"malformed fails closed despite default on", "maybe", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memSettings{values: map[string]string{SettingProjectsSelfCreate: tt.stored}}

			got, err := NewProjectSettings(store, tt.def).SelfCreate(context.Background())
			if err != nil || got != tt.want {
				t.Fatalf("SelfCreate = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestProjectSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := &memSettings{}
	s := NewProjectSettings(store, true)

	for _, on := range []bool{false, true, false} {
		if err := s.SetSelfCreate(ctx, on); err != nil {
			t.Fatal(err)
		}

		got, err := s.SelfCreate(ctx)
		if err != nil || got != on {
			t.Fatalf("after SetSelfCreate(%v): %v, %v", on, got, err)
		}
	}
}

func TestProjectSettingsStoreErrors(t *testing.T) {
	ctx := context.Background()

	s := NewProjectSettings(&memSettings{getErr: errSettingStore}, true)
	if got, err := s.SelfCreate(ctx); !errors.Is(err, errSettingStore) || got {
		t.Fatalf("SelfCreate = %v, %v", got, err)
	}

	s = NewProjectSettings(&memSettings{setErr: errSettingStore}, true)
	if err := s.SetSelfCreate(ctx, true); !errors.Is(err, errSettingStore) {
		t.Fatalf("SetSelfCreate err = %v", err)
	}
}

func TestProjectSettingsNilIsOff(t *testing.T) {
	var s *ProjectSettings

	if got, err := s.SelfCreate(context.Background()); err != nil || got {
		t.Fatalf("nil settings = %v, %v", got, err)
	}

	if got, err := NewProjectSettings(nil, true).SelfCreate(context.Background()); err != nil || got {
		t.Fatalf("nil store = %v, %v", got, err)
	}
}
