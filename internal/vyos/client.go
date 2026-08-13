/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package vyos implements a thin client for the VyOS 1.4+ HTTPS API.
// See https://docs.vyos.io/en/latest/automation/vyos-api.html for the upstream
// protocol description.
//
// The client speaks form-encoded POSTs (data + key) and unmarshals the
// JSON envelope returned by all VyOS endpoints.
package vyos

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errUnknownFailure is returned when VyOS replies with success=false but
// supplies no error message.
var errUnknownFailure = errors.New("vyos reported success=false with no error message")

// Operation describes one VyOS configuration mutation that can be sent
// to the /configure endpoint. Value is optional for "delete" operations.
type Operation struct {
	Op    string   `json:"op"`
	Path  []string `json:"path"`
	Value string   `json:"value,omitempty"`
}

// Op values accepted by /configure.
const (
	OpSet    = "set"
	OpDelete = "delete"
)

// ShowRequest describes a single read operation against /show or /retrieve.
type ShowRequest struct {
	Op   string   `json:"op"`
	Path []string `json:"path"`
}

// envelope is the standard wrapper that VyOS returns on every endpoint.
type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Client talks to the VyOS HTTPS API on a single endpoint.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// Option mutates a Client during construction.
type Option func(*Client)

// WithHTTPClient overrides the underlying *http.Client (default: 30s timeout,
// system TLS, no proxy). Tests use this to plug in an httptest server.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		c.http = httpClient
	}
}

// WithInsecureSkipVerify disables TLS hostname/CA verification. VyOS
// installations typically ship with a self-signed certificate; the
// controller compensates by trusting only the in-band API token.
// Production deployments that pin a CA should pass WithHTTPClient instead.
//
// The transport is cloned from http.DefaultTransport so cluster-wide
// proxy settings (HTTPS_PROXY, NO_PROXY) and the rest of the default
// behaviour (dial timeouts, keep-alives, connection pooling) survive.
// Only TLSClientConfig is replaced.
func WithInsecureSkipVerify() Option {
	return func(c *Client) {
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			clone := base.Clone()
			clone.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // VyOS self-signed cert; in-band token authenticates the channel
			c.http.Transport = clone

			return
		}

		// Fallback when callers have replaced http.DefaultTransport with
		// something that is not an *http.Transport.
		c.http.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // VyOS self-signed cert; in-band token authenticates the channel
		}
	}
}

// NewClient builds a client targeting baseURL (no trailing slash) and
// authenticating with apiKey on every request.
func NewClient(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Configure submits a batch of set/delete operations against /configure
// in a single transaction. Empty batches are a no-op.
func (c *Client) Configure(ctx context.Context, ops []Operation) error {
	if len(ops) == 0 {
		return nil
	}

	if _, err := c.post(ctx, "/configure", ops); err != nil {
		return fmt.Errorf("vyos configure: %w", err)
	}

	return nil
}

// Show issues a /show query and returns the raw JSON data field from
// the response envelope (typically a string).
func (c *Client) Show(ctx context.Context, path []string) (json.RawMessage, error) {
	data, err := c.post(ctx, "/show", ShowRequest{Op: "show", Path: path})
	if err != nil {
		return nil, fmt.Errorf("vyos show: %w", err)
	}

	return data, nil
}

// ShowVPNIPSecSA fetches `show vpn ipsec sa` and parses the textual
// output into IPSec peer observations. Returns an empty slice when the
// output is empty (no peers active) — never nil.
func (c *Client) ShowVPNIPSecSA(ctx context.Context) ([]IPSecObservation, error) {
	raw, err := c.Show(ctx, []string{"vpn", "ipsec", "sa"})
	if err != nil {
		return nil, err
	}

	text, err := decodeShowText(raw)
	if err != nil {
		return nil, fmt.Errorf("decode vpn ipsec sa: %w", err)
	}

	return ParseIPSecSA(text), nil
}

// ShowInterfacesDetail fetches `show interfaces detail` and parses the
// physical-ethernet device ↔ MAC pairs out of the `ip addr`-style
// output. Returns an empty slice when VyOS reports nothing — never nil.
func (c *Client) ShowInterfacesDetail(ctx context.Context) ([]EthernetObservation, error) {
	raw, err := c.Show(ctx, []string{"interfaces", "detail"})
	if err != nil {
		return nil, err
	}

	text, err := decodeShowText(raw)
	if err != nil {
		return nil, fmt.Errorf("decode interfaces detail: %w", err)
	}

	return ParseInterfacesDetail(text), nil
}

// ShowBGPSummary fetches `show bgp summary` and parses the FRR table.
func (c *Client) ShowBGPSummary(ctx context.Context) ([]BGPObservation, error) {
	raw, err := c.Show(ctx, []string{"bgp", "summary"})
	if err != nil {
		return nil, err
	}

	text, err := decodeShowText(raw)
	if err != nil {
		return nil, fmt.Errorf("decode bgp summary: %w", err)
	}

	return ParseBGPSummary(text), nil
}

// decodeShowText handles the two shapes a /show data field can take:
// a JSON-encoded string ("..."), or a bare JSON null when VyOS has
// nothing to report.
func decodeShowText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}

	return s, nil
}

// Retrieve issues a /retrieve query for the current configuration tree
// at the given path. Returns the raw JSON data field.
func (c *Client) Retrieve(ctx context.Context, path []string) (json.RawMessage, error) {
	data, err := c.post(ctx, "/retrieve", ShowRequest{Op: "showConfig", Path: path})
	if err != nil {
		return nil, fmt.Errorf("vyos retrieve: %w", err)
	}

	return data, nil
}

// post is the shared transport for every VyOS endpoint. The payload is
// JSON-marshalled into the form-encoded `data` field, the API key is
// sent in `key`, and the JSON envelope is unmarshalled into err/data.
func (c *Client) post(ctx context.Context, path string, payload any) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	form := url.Values{
		"data": []string{string(encoded)},
		"key":  []string{c.apiKey},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req) //nolint:gosec // VyOS endpoint is controller-supplied (router design-doc) — by-design call to a configured URL, not user-tainted input
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("vyos %s returned http %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w (body=%s)", err, truncate(string(body), 200))
	}

	if !env.Success {
		if env.Error != "" {
			return nil, fmt.Errorf("vyos error: %s", env.Error)
		}

		return nil, errUnknownFailure
	}

	return env.Data, nil
}

// truncate clips a string to at most n bytes, appending "…" if cut.
// Used in error messages to keep failure traces bounded.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "…"
}
