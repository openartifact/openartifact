// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !noai

package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/ai"
)

var errSettingsDown = errors.New("database unavailable")

// settingsDB is the system_settings table another process writes to.
type settingsDB struct {
	mu  sync.Mutex
	m   map[string]string
	err error
}

func newSettingsDB() *settingsDB { return &settingsDB{m: map[string]string{}} }

func (s *settingsDB) set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.m[key] = value
}

func (s *settingsDB) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.err = err
}

func (s *settingsDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return "", s.err
	}

	return s.m[key], nil
}

// enable stores the settings an admin saves to turn AI on with model.
func (s *settingsDB) enable(model string) {
	s.set(ai.SettingEnabled, "true")
	s.set(ai.SettingAutoAnalyze, "true")
	s.set(ai.SettingProvider, "custom")
	s.set(ai.SettingBaseURL, "http://127.0.0.1:1")
	s.set(ai.SettingModel, model)
}

func modelOf(w *AIAnalysisWorker) string {
	c := w.rt.Load().client
	if c == nil {
		return ""
	}

	return c.Model()
}

// A process that started with AI off picks up the settings another process
// saved, follows a model change and a switch-off, without a restart.
func TestSyncSettingsFollowsSettingsSavedByAnotherProcess(t *testing.T) {
	t.Parallel()

	db := newSettingsDB()
	env := ai.Settings{}
	w := NewAIAnalysisWorker(nil, nil, testLog(t))
	w.Configure(ai.LoadSettings(t.Context(), db, env))

	if w.Enabled() {
		t.Fatal("worker enabled before any setting was saved")
	}

	db.enable("model-a")
	w.syncSettings(t.Context(), db, env)

	if !w.Enabled() || !w.AutoAnalyze() || modelOf(w) != "model-a" {
		t.Fatalf(
			"after enabling: enabled=%v auto=%v model=%q, want enabled, auto-analyze and model-a",
			w.Enabled(),
			w.AutoAnalyze(),
			modelOf(w),
		)
	}

	db.set(ai.SettingModel, "model-b")
	w.syncSettings(t.Context(), db, env)

	if got := modelOf(w); got != "model-b" {
		t.Fatalf("model after the change = %q, want model-b", got)
	}

	db.set(ai.SettingEnabled, "false")
	w.syncSettings(t.Context(), db, env)

	if w.Enabled() || w.AutoAnalyze() {
		t.Fatalf("after disabling: enabled=%v auto=%v, want both off", w.Enabled(), w.AutoAnalyze())
	}
}

func TestSyncSettingsKeepsTheClientWhenNothingChanged(t *testing.T) {
	t.Parallel()

	db := newSettingsDB()
	db.enable("model-a")

	env := ai.Settings{}
	w := NewAIAnalysisWorker(nil, nil, testLog(t))
	w.Configure(ai.LoadSettings(t.Context(), db, env))

	before := w.rt.Load()

	w.syncSettings(t.Context(), db, env)

	if w.rt.Load() != before {
		t.Fatal("unchanged settings rebuilt the client")
	}
}

// LoadSettings reads an unreadable key as unset, so a database outage looks like
// "back to the env settings". The poll must not apply that.
func TestSyncSettingsKeepsTheSettingsWhenTheDatabaseFails(t *testing.T) {
	t.Parallel()

	db := newSettingsDB()
	db.enable("model-a")

	env := ai.Settings{}
	w := NewAIAnalysisWorker(nil, nil, testLog(t))
	w.Configure(ai.LoadSettings(t.Context(), db, env))

	db.fail(errSettingsDown)
	w.syncSettings(t.Context(), db, env)

	if !w.Enabled() || modelOf(w) != "model-a" {
		t.Fatalf("after a failed read: enabled=%v model=%q, want the saved settings kept", w.Enabled(), modelOf(w))
	}

	db.fail(nil)
	db.set(ai.SettingModel, "model-b")
	w.syncSettings(t.Context(), db, env)

	if got := modelOf(w); got != "model-b" {
		t.Fatalf("model once the database is back = %q, want model-b", got)
	}
}

func TestSyncSettingsLoopStopsWithItsContext(t *testing.T) {
	t.Parallel()

	db := newSettingsDB()
	env := ai.Settings{}
	w := NewAIAnalysisWorker(nil, nil, testLog(t))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		w.SyncSettings(ctx, db, env, time.Millisecond)
	}()

	db.enable("model-a")
	waitFor(t, "the loop to apply the saved settings", func() bool { return modelOf(w) == "model-a" })

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SyncSettings did not return after its context ended")
	}
}
