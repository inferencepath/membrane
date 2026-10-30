// Package consolecli is a command-line client for Membrane's declarative
// configuration. It talks to the Console Admin API (export, diff, apply) and
// never opens the store, so the daemon remains its only writer and the CLI
// works against a local or remote Console.
//
// The admin token is resolved from an env: or file: reference and never
// printed. Bundles carry credential references and caller hashes, not values.
package consolecli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/inferencepath/membrane/internal/server/console"
)

// DefaultConsoleURL is the loopback Admin API the daemon binds by default.
const DefaultConsoleURL = "http://127.0.0.1:8300"

const (
	pathExport = "/api/console/config/export"
	pathDiff   = "/api/console/config/diff"
	pathApply  = "/api/console/config/apply"
)

// Client is an HTTP client of the Console's declarative-config Admin API.
type Client struct {
	BaseURL string
	Token   string // resolved admin token (value); empty for loopback-only access
	HTTP    *http.Client
}

// NewClient returns a Client with a default timeout.
func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) do(method, path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("X-Console-Token", c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.HTTP.Do(req)
}

// Export fetches the live configuration bundle and its version token.
func (c *Client) Export() (raw []byte, version string, err error) {
	resp, err := c.do(http.MethodGet, pathExport, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, "", statusError("export", resp.StatusCode, b)
	}
	return b, resp.Header.Get("X-Config-Version"), nil
}

// Diff validates a candidate bundle against the live store and returns the
// per-kind change set without writing.
func (c *Client) Diff(bundle []byte) (console.BundleDiff, error) {
	resp, err := c.do(http.MethodPost, pathDiff, bundle)
	if err != nil {
		return console.BundleDiff{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return console.BundleDiff{}, statusError("diff", resp.StatusCode, b)
	}
	var diff console.BundleDiff
	if err := json.Unmarshal(b, &diff); err != nil {
		return console.BundleDiff{}, fmt.Errorf("diff: decode response: %w", err)
	}
	return diff, nil
}

// Apply validates a candidate bundle, checks version, writes atomically, and
// triggers a reload. An empty version skips the check.
func (c *Client) Apply(bundle []byte, version string) (console.ApplyResult, error) {
	body, err := json.Marshal(struct {
		Raw     string `json:"raw"`
		Version string `json:"version"`
	}{Raw: string(bundle), Version: version})
	if err != nil {
		return console.ApplyResult{}, err
	}
	resp, err := c.do(http.MethodPost, pathApply, body)
	if err != nil {
		return console.ApplyResult{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return console.ApplyResult{}, statusError("apply", resp.StatusCode, b)
	}
	var res console.ApplyResult
	if err := json.Unmarshal(b, &res); err != nil {
		return console.ApplyResult{}, fmt.Errorf("apply: decode response: %w", err)
	}
	return res, nil
}

// statusError turns a non-200 Admin API response into an error, using the
// server's JSON "error" message when present.
func statusError(op string, code int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("%s: %s (HTTP %d)", op, e.Error, code)
	}
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("%s: unauthorized (set --token-ref to the admin token reference) (HTTP %d)", op, code)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("%s: declarative config is not available on the server (HTTP %d)", op, code)
	default:
		return fmt.Errorf("%s: unexpected response (HTTP %d)", op, code)
	}
}
