// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

// version is the build version Main receives from the binary that embeds the
// CLI (cmd/oa and the server's `openartifact oa`).
var version = "dev"

// ─── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	// TokenID is the server-side id of the token minted by `auth login`, so
	// `auth logout --revoke` can revoke it.
	TokenID string `json:"token_id,omitempty"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "oa", "config.json")
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Server: "http://localhost:8080"}, nil
		}

		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.Server == "" {
		cfg.Server = "http://localhost:8080"
	}

	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(configPath(), data, configDirMode, configFileMode)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// ─── HTTP Client ─────────────────────────────────────────────────────────────

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newClient(serverURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(serverURL, "/"),
		token:      token,
		httpClient: newHTTPClient(),
	}
}

func (c *Client) do(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	//nolint:noctx // CLI tool, no context available
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	return c.httpClient.Do(req)
}

func (c *Client) doJSON(method, path string, body any) (*http.Response, error) {
	var r io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}

		r = bytes.NewReader(data)
	}

	return c.do(method, path, r, "application/json")
}

// ─── Output helpers ───────────────────────────────────────────────────────────

func dief(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}

func printError(resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)

	fmt.Fprintf(os.Stderr, "Error (%d): %s\n", resp.StatusCode, errorMessage(body))
}

// errorMessage extracts the human readable message of an API error body (its
// message, error or details field), falling back to the raw body.
func errorMessage(body []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Details string `json:"details"`
	}

	_ = json.Unmarshal(body, &e)

	msg := e.Message
	if msg == "" {
		msg = e.Error
	}

	if msg == "" {
		msg = e.Details
	}

	if msg == "" {
		msg = string(body)
	}

	return msg
}

func checkOK(resp *http.Response, expected ...int) {
	codes := expected
	if len(codes) == 0 {
		codes = []int{http.StatusOK}
	}

	if slices.Contains(codes, resp.StatusCode) {
		return
	}

	printError(resp)
	os.Exit(1)
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
		return
	}

	fmt.Println(string(data))
}

func decodeJSON(resp *http.Response, v any) {
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		dief("decoding response: %v", err)
	}
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Generic helpers
// ═══════════════════════════════════════════════════════════════════════════════

// getJSON issues a GET and decodes the (200) JSON response into v.
func getJSON(client *Client, path string, v any) {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)
	decodeJSON(resp, v)
}

// sendJSON issues a request with a JSON body (nil for none), requires one of
// the expected statuses, and returns the decoded object response (nil when the
// response has no JSON object body, e.g. 204).
func sendJSON(client *Client, method, path string, body any, expected ...int) map[string]any {
	resp, err := client.doJSON(method, path, body)
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, expected...)

	data, _ := io.ReadAll(resp.Body)

	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return nil
	}

	return out
}

// listItems extracts the array under listKey from an object response, or
// accepts a bare array. Any other shape is an error rather than a guess.
var errNoList = errors.New("response has no list under key")

func listItems(body []byte, listKey string) ([]map[string]any, error) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapper); err == nil {
		raw, ok := wrapper[listKey]
		if !ok {
			return nil, fmt.Errorf("%w %q", errNoList, listKey)
		}

		if string(raw) == "null" {
			return nil, nil
		}

		var items []map[string]any
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("decoding %q list: %w", listKey, err)
		}

		return items, nil
	}

	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
	}

	return items, nil
}

// fetchList GETs path and returns the list under listKey.
func fetchList(client *Client, path, listKey string) []map[string]any {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	body, _ := io.ReadAll(resp.Body)

	items, err := listItems(body, listKey)
	if err != nil {
		dief("%v", err)
	}

	return items
}

// fetchAllPages walks a page/size paginated endpoint (response carries "total"
// and the list under listKey) and returns every item. sizeParam is the page
// size query parameter ("limit" or "per_page").
func fetchAllPages(client *Client, path, listKey, sizeParam string, size int) []map[string]any {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}

	var all []map[string]any

	for page := 1; ; page++ {
		resp, err := client.do("GET", fmt.Sprintf("%s%s%s=%d&page=%d", path, sep, sizeParam, size, page), nil, "")
		if err != nil {
			dief("%v", err)
		}

		checkOK(resp)

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		items, err := listItems(body, listKey)
		if err != nil {
			dief("%v", err)
		}

		var meta struct {
			Total int `json:"total"`
		}
		_ = json.Unmarshal(body, &meta)

		all = append(all, items...)

		if len(items) == 0 || len(items) < size || (meta.Total > 0 && len(all) >= meta.Total) {
			return all
		}
	}
}

// listGeneric fetches a list endpoint whose response is {"<listKey>": [...]}
// (or a bare array) and prints a table.
func listGeneric(client *Client, path, listKey string, headers, fields []string, outFmt string) {
	printTable(fetchList(client, path, listKey), headers, fields, outFmt)
}

func printTable(items []map[string]any, headers, fields []string, outFmt string) {
	if outFmt == "json" {
		if items == nil {
			items = []map[string]any{}
		}

		printJSON(items)

		return
	}

	w := newTabWriter()
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))

	for _, item := range items {
		vals := make([]string, len(fields))
		for i, f := range fields {
			vals[i] = cell(item[f])
		}

		_, _ = fmt.Fprintln(w, strings.Join(vals, "\t"))
	}

	_ = w.Flush()
}

// cell renders a JSON value for a table: nil as "-", numbers without an
// exponent, everything else with %v.
func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func showGeneric(client *Client, path, outFmt string) {
	var result map[string]any
	getJSON(client, path, &result)
	printObject(result, outFmt)
}

// printObject prints a JSON object as indented JSON or as sorted key/value rows.
func printObject(result map[string]any, outFmt string) {
	if outFmt == "json" {
		printJSON(result)
		return
	}

	keys := make([]string, 0, len(result))
	for k := range result {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	w := newTabWriter()
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", k, cell(result[k]))
	}

	_ = w.Flush()
}

func downloadFile(client *Client, path, dest string) {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	n, err := downloadTo(resp, dest)
	if err != nil {
		dief("%v", err)
	}

	fmt.Printf("Downloaded %d bytes to '%s'.\n", n, dest)
}

// escape makes a user-supplied value safe as one URL path segment. A value that
// starts with - can only get here after a bare -- (the flag parser takes every
// other one for a flag), so it is a name like any other.
func escape(s string) string { return url.PathEscape(s) }

// warnCredentialExposure prints warnings on w for the two ways the CLI can leak
// its token: a --token argument (visible to every local user in the process
// list and kept in shell history) and a token sent over plain http to a
// non-loopback server.
func warnCredentialExposure(w io.Writer, serverURL, token string, tokenFlagSet bool) {
	if token == "" {
		return
	}

	if tokenFlagSet {
		fmt.Fprintln(w, "Warning: --token is visible in the process list and shell history; prefer OA_TOKEN or `oa auth login`.")
	}

	if u, err := url.Parse(serverURL); err == nil && u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		fmt.Fprintf(w, "Warning: sending the API token over plain http to %s; use https.\n", u.Host)
	}
}
