// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
)

var (
	errNoChoices    = errors.New("no choices in response")
	errAPIStatus    = errors.New("llm api returned non-200 status")
	errAPIError     = errors.New("llm api returned an error")
	errBadLLMOutput = errors.New("llm returned unparseable output")
)

// maxManifestBytes bounds the untrusted manifest text sent to the LLM.
const maxManifestBytes = 64 * 1024

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

const defaultSystemPrompt = `You are an expert DevOps and Software Architecture AI embedded within a corporate Artifact Manager. Your job is to analyze uploaded software packages (via their manifest and dependency files), assess them for quality, and generate searchable metadata.

You will be provided with the raw text of package manifests (e.g., package.json, pom.xml), readmes, or dependency trees.

Perform the following tasks:
1. Summary: Write a concise, 2-sentence description of what this package does.
2. Categorization: Generate an array of 3 to 5 highly relevant technical tags for search indexing.
3. License Check: Identify the license. Flag "SAFE" for permissive licenses (MIT, Apache 2.0, BSD) and "REVIEW_REQUIRED" for copyleft or missing licenses (GPL, AGPL).
4. Dependency Health: Note any glaringly outdated or notoriously insecure dependencies if visible in the manifest.

### STRICT OUTPUT FORMAT ###
You must respond ONLY with a valid, minified JSON object matching the exact structure below. Do not include markdown formatting like ` + "```" + `json, greetings, or conversational text.

{"artifact_summary":"string","tags":["string","string","string"],"license_status":"SAFE | REVIEW_REQUIRED | UNKNOWN","license_name":"string","security_notes":"string","quarantine_recommended":false}`

// AnalysisResult is the structured JSON the LLM must return.
type AnalysisResult struct {
	ArtifactSummary       string   `json:"artifact_summary"`
	Tags                  []string `json:"tags"`
	LicenseStatus         string   `json:"license_status"`
	LicenseName           string   `json:"license_name"`
	SecurityNotes         string   `json:"security_notes"`
	QuarantineRecommended bool     `json:"quarantine_recommended"`
}

// UsageInfo holds token usage and the serving model from the API response.
type UsageInfo struct {
	PromptTokens     int
	CompletionTokens int
	// Model is the model the provider reports having served the request
	// (e.g. a dated snapshot), or the configured model when it reports none.
	Model string
}

// Config holds AI client configuration.
type Config struct {
	Provider     string // "openai", "azure", "ollama", "custom" (any OpenAI-compatible endpoint; BaseURL required)
	BaseURL      string // override endpoint
	APIKey       string
	Model        string
	MaxTokens    int
	TimeoutSecs  int
	SystemPrompt string
}

// Client is an OpenAI-compatible LLM client.
type Client struct {
	cfg          Config
	httpClient   *http.Client
	baseURL      string
	systemPrompt string
}

// NewClient creates a new AI client from config.
func NewClient(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		switch cfg.Provider {
		case "azure", "custom":
			// Azure and custom endpoints require BaseURL to be set explicitly;
			// leaving it empty fails at request time instead of silently
			// sending the key to api.openai.com.
			baseURL = cfg.BaseURL
		case "ollama":
			baseURL = "http://localhost:11434/v1"
		default: // openai
			baseURL = "https://api.openai.com/v1"
		}
	}

	baseURL = strings.TrimRight(baseURL, "/")

	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	if key, err := crypto.Decrypt(cfg.APIKey); err == nil {
		cfg.APIKey = key
	}

	return &Client{
		cfg:          cfg,
		httpClient:   telemetry.WrapClient(netutil.SafeHTTPClient(timeout)),
		baseURL:      baseURL,
		systemPrompt: systemPrompt,
	}
}

// Model returns the configured model name.
func (c *Client) Model() string { return c.cfg.Model }

// chatRequest is the OpenAI chat completions request body.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Analyze sends manifest text to the LLM and returns structured analysis.
func (c *Client) Analyze(ctx context.Context, manifestText string) (*AnalysisResult, *UsageInfo, error) {
	manifestText = truncateUTF8(manifestText, maxManifestBytes)
	reqBody := chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: c.systemPrompt},
			{Role: "user", Content: manifestText},
		},
		MaxTokens:   c.cfg.MaxTokens,
		Temperature: 0.1, // low temperature for deterministic JSON output
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := c.baseURL + "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("http request: %w", err)
	}

	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%w: status %d", errAPIStatus, resp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return nil, nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if chatResp.Error != nil {
		return nil, nil, errAPIError
	}

	if len(chatResp.Choices) == 0 {
		return nil, nil, errNoChoices
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	// Strip markdown code fences if the model ignores instructions
	content = stripMarkdownFences(content)

	var result AnalysisResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errBadLLMOutput, err)
	}

	result.ArtifactSummary = truncateUTF8(result.ArtifactSummary, 2000)
	result.SecurityNotes = truncateUTF8(result.SecurityNotes, 4000)
	result.LicenseName = truncateUTF8(result.LicenseName, 200)
	if len(result.Tags) > 10 {
		result.Tags = result.Tags[:10]
	}
	for i := range result.Tags {
		result.Tags[i] = truncateUTF8(result.Tags[i], 64)
	}

	// Normalize license_status
	switch strings.ToUpper(result.LicenseStatus) {
	case "SAFE":
		result.LicenseStatus = "SAFE"
	case "REVIEW_REQUIRED":
		result.LicenseStatus = "REVIEW_REQUIRED"
	default:
		result.LicenseStatus = "UNKNOWN"
	}

	usage := &UsageInfo{
		PromptTokens:     chatResp.Usage.PromptTokens,
		CompletionTokens: chatResp.Usage.CompletionTokens,
		Model:            c.cfg.Model,
	}

	// The response is untrusted input: bound the reported model name.
	if m := strings.TrimSpace(chatResp.Model); m != "" {
		usage.Model = truncateUTF8(m, 200)
	}

	return &result, usage, nil
}

// do validates the request URL against the SSRF policy (scheme, resolved
// addresses) and sends it through the SSRF-safe client, which re-checks the
// destination at dial time. Private/loopback endpoints (e.g. a local Ollama)
// require OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	if err := netutil.ValidateOutboundURLContext(req.Context(), req.URL.String()); err != nil {
		return nil, fmt.Errorf("ai endpoint rejected: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return resp, nil
}

// ListModels fetches the list of available models from the API.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	// Create a context with 10s timeout if not already shorter
	ctxWithTimeout := ctx
	if deadline, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc

		ctxWithTimeout, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	} else {
		// Check if existing deadline is more than 10s away
		remaining := time.Until(deadline)
		if remaining > 10*time.Second {
			var cancel context.CancelFunc

			ctxWithTimeout, cancel = context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
		}
	}

	endpoint := c.baseURL + "/models"

	req, err := http.NewRequestWithContext(ctxWithTimeout, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBytes)) //nolint:err113 // dynamic error with runtime context
	}

	type modelsResponse struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	var modelsResp modelsResponse
	if err := json.Unmarshal(respBytes, &modelsResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	// Extract model IDs and sort
	models := make([]string, len(modelsResp.Data))
	for i, m := range modelsResp.Data {
		models[i] = m.ID
	}

	// Sort alphabetically
	sort.Strings(models)

	return models, nil
}

func stripMarkdownFences(s string) string {
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")

	return strings.TrimSpace(s)
}
