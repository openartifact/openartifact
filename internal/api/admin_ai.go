// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

type AdminAIHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	aiWorker  *worker.AIAnalysisWorker
	envAI     ai.Settings
}

func NewAdminAIHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminAIHandler {
	return &AdminAIHandler{db: db, log: log, evaluator: evaluator}
}

// WithRuntime makes configuration changes take effect immediately: after every
// saved change the worker's client and auto-analysis switch are rebuilt from
// the stored settings layered over env (the env/YAML defaults).
func (h *AdminAIHandler) WithRuntime(w *worker.AIAnalysisWorker, env ai.Settings) *AdminAIHandler {
	h.aiWorker = w
	h.envAI = env

	return h
}

// reload applies the stored settings to the running worker.
func (h *AdminAIHandler) reload(ctx context.Context) {
	if h.aiWorker == nil {
		return
	}

	s := ai.LoadSettings(ctx, h.db, h.envAI)
	h.aiWorker.Configure(s)
	h.log.Infow("AI settings applied", "enabled", s.Enabled, "auto_analyze", s.AutoAnalyze,
		"provider", s.Config.Provider, "model", s.Config.Model)
}

func (h *AdminAIHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/ai/config", h.handleGetConfig)
	r.Put("/api/v1/admin/ai/config", h.handlePutConfig)
	r.Post("/api/v1/admin/ai/test", h.handleTest)
	r.Get("/api/v1/admin/ai/models", h.handleGetModels)
}

func (h *AdminAIHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return false
	}

	if !isAdmin {
		writeError(w, http.StatusForbidden, "admin_required", "admin access required")
		return false
	}

	return true
}

// loadConfigFromDB reads AI config from system_settings table.
func (h *AdminAIHandler) loadConfigFromDB(ctx context.Context) ai.Config {
	cfg := ai.Config{}

	// Read each setting
	// Note: enabled is not stored in ai.Config, it's handled separately

	provider, _ := h.db.GetSystemSetting(ctx, ai.SettingProvider)
	cfg.Provider = provider

	baseURL, _ := h.db.GetSystemSetting(ctx, ai.SettingBaseURL)
	cfg.BaseURL = baseURL

	apiKey, _ := h.db.GetSystemSetting(ctx, ai.SettingAPIKey)
	cfg.APIKey = apiKey

	model, _ := h.db.GetSystemSetting(ctx, ai.SettingModel)
	cfg.Model = model

	maxTokensStr, _ := h.db.GetSystemSetting(ctx, ai.SettingMaxTokens)
	if maxTokensStr != "" {
		if n, err := strconv.Atoi(maxTokensStr); err == nil {
			cfg.MaxTokens = n
		}
	}

	timeoutSecsStr, _ := h.db.GetSystemSetting(ctx, ai.SettingTimeoutSecs)
	if timeoutSecsStr != "" {
		if n, err := strconv.Atoi(timeoutSecsStr); err == nil {
			cfg.TimeoutSecs = n
		}
	}

	systemPrompt, _ := h.db.GetSystemSetting(ctx, ai.SettingSystemPrompt)
	cfg.SystemPrompt = systemPrompt

	return cfg
}

func (h *AdminAIHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	// Report what the server runs with: stored settings layered over env/YAML,
	// the same rules the worker applies (ai.LoadSettings), so a deployment that
	// enables AI through env is not shown as disabled.
	eff := ai.LoadSettings(r.Context(), h.db, h.envAI)
	cfg := eff.Config

	// Mask API key
	apiKeyDisplay := ""
	if cfg.APIKey != "" {
		apiKeyDisplay = "••••••••" //nolint:gosec // This is a display mask, not a real credential
	}

	response := map[string]any{
		"enabled":       eff.Enabled,
		"auto_analyze":  eff.AutoAnalyze,
		"provider":      cfg.Provider,
		"base_url":      cfg.BaseURL,
		"api_key":       apiKeyDisplay,
		"has_api_key":   cfg.APIKey != "",
		"model":         cfg.Model,
		"max_tokens":    cfg.MaxTokens,
		"timeout_secs":  cfg.TimeoutSecs,
		"system_prompt": cfg.SystemPrompt,
	}

	writeJSON(w, http.StatusOK, response)
}

const (
	aiAPIKeyMask       = "••••••••" //nolint:gosec // G101: display placeholder, not a credential
	aiMaxTokensLimit   = 200000
	aiMaxTimeoutSecs   = 600
	aiMaxSystemPrompt  = 16 * 1024
	aiMaxFieldLen      = 2048
	aiProviderOpenAI   = "openai"
	aiProviderAzure    = "azure"
	aiProviderOllama   = "ollama"
	aiProviderCustom   = "custom"
	aiTestFailedReason = "could not list models from the AI provider; check the endpoint, credentials and server logs"
)

// aiConfigPatch is a partial AI configuration. Every field is a pointer so an
// omitted field leaves the stored value untouched, while an explicit empty
// value clears it.
type aiConfigPatch struct {
	Enabled      *bool   `json:"enabled"`
	AutoAnalyze  *bool   `json:"auto_analyze"`
	Provider     *string `json:"provider"`
	BaseURL      *string `json:"base_url"`
	APIKey       *string `json:"api_key"`
	Model        *string `json:"model"`
	MaxTokens    *int    `json:"max_tokens"`
	TimeoutSecs  *int    `json:"timeout_secs"`
	SystemPrompt *string `json:"system_prompt"`
}

// validate checks the patch and returns a message for the first problem.
func (p *aiConfigPatch) validate() string {
	if msg := p.validateEndpoint(); msg != "" {
		return msg
	}

	for name, v := range map[string]*string{"base_url": p.BaseURL, "model": p.Model, "api_key": p.APIKey} {
		if v != nil && len(*v) > aiMaxFieldLen {
			return name + " is too long"
		}
	}

	if p.SystemPrompt != nil && len(*p.SystemPrompt) > aiMaxSystemPrompt {
		return "system_prompt is too long"
	}

	return p.validateLimits()
}

// validateEndpoint checks the provider and base URL fields.
func (p *aiConfigPatch) validateEndpoint() string {
	if p.Provider != nil && *p.Provider != "" &&
		*p.Provider != aiProviderOpenAI && *p.Provider != aiProviderAzure &&
		*p.Provider != aiProviderOllama && *p.Provider != aiProviderCustom {
		return "provider must be one of: openai, azure, ollama, custom"
	}

	if p.BaseURL != nil && *p.BaseURL != "" {
		u, err := url.Parse(*p.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return "base_url must be an absolute http(s) URL"
		}
	}

	return ""
}

// validateLimits checks the numeric fields.
func (p *aiConfigPatch) validateLimits() string {
	if p.MaxTokens != nil && (*p.MaxTokens < 0 || *p.MaxTokens > aiMaxTokensLimit) {
		return "max_tokens must be between 0 and " + strconv.Itoa(aiMaxTokensLimit)
	}

	if p.TimeoutSecs != nil && (*p.TimeoutSecs < 0 || *p.TimeoutSecs > aiMaxTimeoutSecs) {
		return "timeout_secs must be between 0 and " + strconv.Itoa(aiMaxTimeoutSecs)
	}

	return ""
}

// toConfig converts the patch (Provider must be non-nil) into an ai.Config.
func (p *aiConfigPatch) toConfig() ai.Config {
	cfg := ai.Config{Provider: *p.Provider}

	if p.BaseURL != nil {
		cfg.BaseURL = *p.BaseURL
	}

	if p.APIKey != nil {
		cfg.APIKey = *p.APIKey
	}

	if p.Model != nil {
		cfg.Model = *p.Model
	}

	if p.MaxTokens != nil {
		cfg.MaxTokens = *p.MaxTokens
	}

	if p.TimeoutSecs != nil {
		cfg.TimeoutSecs = *p.TimeoutSecs
	}

	if p.SystemPrompt != nil {
		cfg.SystemPrompt = *p.SystemPrompt
	}

	return cfg
}

// patchedTestConfig builds the config to test from a request patch. It returns
// a validation message when the patch is not acceptable.
func (h *AdminAIHandler) patchedTestConfig(ctx context.Context, body *aiConfigPatch) (ai.Config, string) {
	if msg := body.validate(); msg != "" {
		return ai.Config{}, msg
	}

	cfg := body.toConfig()

	// A client-supplied endpoint must not let an admin session probe or
	// send credentials to internal addresses.
	if cfg.BaseURL != "" {
		if err := netutil.ValidateOutboundURL(cfg.BaseURL); err != nil {
			return ai.Config{}, "base_url is not allowed: " + err.Error()
		}
	}

	// A masked key means "use the stored one".
	if cfg.APIKey == aiAPIKeyMask || cfg.APIKey == "" {
		stored, _ := h.db.GetSystemSetting(ctx, ai.SettingAPIKey)
		cfg.APIKey = stored
	}

	return cfg, ""
}

func (h *AdminAIHandler) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	var body aiConfigPatch

	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := body.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if msg := h.customNeedsBaseURL(r.Context(), &body); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	// Keys and values are kept in separate slices so the (secret) values are
	// never reachable from anything that gets logged.
	keys := make([]string, 0, 8)
	vals := make([]string, 0, 8)

	add := func(k, v string) {
		keys = append(keys, k)
		vals = append(vals, v)
	}

	if body.Enabled != nil {
		add(ai.SettingEnabled, strconv.FormatBool(*body.Enabled))
	}

	if body.AutoAnalyze != nil {
		add(ai.SettingAutoAnalyze, strconv.FormatBool(*body.AutoAnalyze))
	}

	if body.Provider != nil {
		add(ai.SettingProvider, *body.Provider)
	}

	if body.BaseURL != nil {
		add(ai.SettingBaseURL, *body.BaseURL)
	}

	// The API key is write-only: the masked placeholder returned by GET means
	// "unchanged", an empty string clears it, anything else is encrypted.
	if body.APIKey != nil && *body.APIKey != aiAPIKeyMask {
		stored := ""

		if *body.APIKey != "" {
			if err := crypto.CheckPlaintext(*body.APIKey); err != nil {
				writeError(w, http.StatusBadRequest, "validation_failed", "api_key must be the plaintext key")

				return
			}

			enc, err := crypto.Encrypt(*body.APIKey)
			if err != nil {
				h.log.Errorw("admin ai: encrypt api key failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal_error", "failed to save config")

				return
			}

			stored = enc
		}

		add(ai.SettingAPIKey, stored)
	}

	if body.Model != nil {
		add(ai.SettingModel, *body.Model)
	}

	if body.MaxTokens != nil {
		add(ai.SettingMaxTokens, strconv.Itoa(*body.MaxTokens))
	}

	if body.TimeoutSecs != nil {
		add(ai.SettingTimeoutSecs, strconv.Itoa(*body.TimeoutSecs))
	}

	if body.SystemPrompt != nil {
		add(ai.SettingSystemPrompt, *body.SystemPrompt)
	}

	for i, key := range keys {
		if err := h.db.SetSystemSetting(r.Context(), key, vals[i]); err != nil {
			h.log.Errorw("admin ai: save setting failed", "key", key, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to save config")

			return
		}
	}

	h.reload(r.Context())

	h.handleGetConfig(w, r)
}

// customNeedsBaseURL rejects a configuration whose effective provider is
// "custom" but has no endpoint: there is no default to fall back to, and the
// stored API key must not be sent to a provider the admin did not choose.
func (h *AdminAIHandler) customNeedsBaseURL(ctx context.Context, p *aiConfigPatch) string {
	provider, _ := h.db.GetSystemSetting(ctx, ai.SettingProvider)
	if p.Provider != nil {
		provider = *p.Provider
	}

	if provider != aiProviderCustom {
		return ""
	}

	baseURL, _ := h.db.GetSystemSetting(ctx, ai.SettingBaseURL)
	if p.BaseURL != nil {
		baseURL = *p.BaseURL
	}

	if baseURL == "" {
		return "base_url is required for the custom provider"
	}

	return ""
}

func (h *AdminAIHandler) handleTest(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	var body *aiConfigPatch

	// The body is optional: without one, the stored configuration is tested.
	_ = decodeJSON(r, &body)

	var cfg ai.Config

	if body != nil && body.Provider != nil && *body.Provider != "" {
		var msg string

		cfg, msg = h.patchedTestConfig(r.Context(), body)
		if msg != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", msg)
			return
		}
	} else {
		cfg = h.loadConfigFromDB(r.Context())
	}

	models, err := ai.NewClient(cfg).ListModels(r.Context())
	if err != nil {
		h.log.Warnw("admin ai: test failed", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": aiTestFailedReason,
		})

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"models": models,
	})
}

func (h *AdminAIHandler) handleGetModels(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	cfg := h.loadConfigFromDB(r.Context())

	models, err := ai.NewClient(cfg).ListModels(r.Context())
	if err != nil {
		h.log.Warnw("admin ai: list models failed", "error", err)
		writeError(w, http.StatusBadGateway, "upstream_error", aiTestFailedReason)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models": models,
	})
}
