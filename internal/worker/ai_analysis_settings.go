// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !noai

package worker

import (
	"context"
	"time"

	"github.com/openartifact/openartifact/internal/ai"
)

// SyncSettings keeps the worker on the AI settings stored in the database until
// ctx ends: every interval it reads the effective settings (the stored ones
// over env, see ai.LoadSettings) and calls Configure when they differ from the
// ones in use. An admin saves the settings through one process, which applies
// them to its own worker at once; this is how every other process (replicas of
// the API, the worker role) follows, so disabling AI, a model change or a key
// rotation does not wait for a restart.
//
// A read that fails leaves the settings in use alone: the stored ones are not
// known. Two processes applying a change at the same moment can leave this one
// on the older settings until the next interval.
func (w *AIAnalysisWorker) SyncSettings(ctx context.Context, r ai.SettingReader, env ai.Settings, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.syncSettings(ctx, r, env)
		}
	}
}

// syncSettings applies the stored settings when they changed.
func (w *AIAnalysisWorker) syncSettings(ctx context.Context, r ai.SettingReader, env ai.Settings) {
	s, err := ai.LoadSettingsChecked(ctx, r, env)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warnw("ai settings: reading the stored settings failed, keeping the current ones", "error", err)
		}

		return
	}

	if s == w.rt.Load().settings {
		return
	}

	w.Configure(s)
	w.log.Infow("AI settings changed by another process, applied", "enabled", s.Enabled, "auto_analyze", s.AutoAnalyze,
		"provider", s.Config.Provider, "model", s.Config.Model)
}
