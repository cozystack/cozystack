/*
Copyright 2025 The Cozystack Authors.

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

// Package openbaotransit is a minimal client for the subset of the OpenBAO API
// needed to provision per-tenant transit auto-unseal: enabling a transit key,
// writing a scoped ACL policy, and minting/validating a scoped token.
//
// It speaks plain HTTP (authenticated with X-Vault-Token) rather than pulling in
// the full OpenBAO SDK — the operator only needs three idempotent calls.
package openbaotransit

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to a single OpenBAO server with a fixed auth token.
type Client struct {
	addr  string
	token string
	http  *http.Client
}

// New returns a Client for addr authenticating with token. caPEM, when non-empty,
// is the CA bundle used to verify the server certificate.
func New(addr, token string, caPEM []byte) (*Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("openbaotransit: failed to parse CA bundle")
		}
		tlsCfg.RootCAs = pool
	}
	return &Client{
		addr:  addr,
		token: token,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.addr+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// EnsureTransitKey creates the named transit key if it does not already exist.
// Creating an existing key is a no-op, so this is safe to call repeatedly.
func (c *Client) EnsureTransitKey(ctx context.Context, keyName string) error {
	// Creating an existing key is a no-op on the OpenBAO side.
	code, data, err := c.do(ctx, http.MethodPost, "/v1/transit/keys/"+keyName, map[string]string{"type": "aes256-gcm96"})
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusNoContent {
		return fmt.Errorf("create transit key %q: status %d: %s", keyName, code, string(data))
	}
	return nil
}

// WriteUnsealPolicy writes an ACL policy granting encrypt+decrypt on keyName.
func (c *Client) WriteUnsealPolicy(ctx context.Context, policyName, keyName string) error {
	hcl := fmt.Sprintf(`path "transit/encrypt/%s" { capabilities = ["update"] }
path "transit/decrypt/%s" { capabilities = ["update"] }
`, keyName, keyName)
	code, data, err := c.do(ctx, http.MethodPut, "/v1/sys/policies/acl/"+policyName, map[string]string{"policy": hcl})
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusNoContent {
		return fmt.Errorf("write policy %q: status %d: %s", policyName, code, string(data))
	}
	return nil
}

// EnsureKubernetesAuthRole binds a tenant ServiceAccount (saName in saNamespace)
// to policyName via the central Kubernetes auth method, so the tenant OpenBAO can
// obtain a transit-scoped token at startup using its projected SA token — no
// static token is ever minted or stored. period sets the issued token's period.
func (c *Client) EnsureKubernetesAuthRole(ctx context.Context, role, saName, saNamespace, policyName, period string) error {
	body := map[string]any{
		"bound_service_account_names":      []string{saName},
		"bound_service_account_namespaces": []string{saNamespace},
		"token_policies":                   []string{policyName},
		"token_period":                     period,
	}
	code, data, err := c.do(ctx, http.MethodPost, "/v1/auth/kubernetes/role/"+role, body)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusNoContent {
		return fmt.Errorf("write kubernetes auth role %q: status %d: %s", role, code, string(data))
	}
	return nil
}
