/*
Copyright 2024 The Cozystack Authors.

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

package config

import "strings"

// CozystackConfig represents the structure of the cozystack ConfigMap.
type CozystackConfig struct {
	// BundleName specifies which bundle to use (e.g., "paas-full", "distro-full", etc.)
	BundleName string `yaml:"bundle-name,omitempty"`

	// BundleDisable is a comma-separated list of components to disable
	BundleDisable string `yaml:"bundle-disable,omitempty"`

	// BundleEnable is a comma-separated list of optional components to enable
	BundleEnable string `yaml:"bundle-enable,omitempty"`

	// OIDCEnabled indicates whether OIDC is enabled (default: "false")
	OIDCEnabled string `yaml:"oidc-enabled,omitempty"`

	// RootHost specifies the root host for tenant-root namespace
	RootHost string `yaml:"root-host,omitempty"`

	// APIServerEndpoint specifies the API server endpoint
	APIServerEndpoint string `yaml:"api-server-endpoint,omitempty"`

	// ClusterDomain specifies the cluster domain (default: "cozy.local")
	ClusterDomain string `yaml:"cluster-domain,omitempty"`

	// IPv4PodCIDR specifies the IPv4 pod CIDR
	IPv4PodCIDR string `yaml:"ipv4-pod-cidr,omitempty"`

	// IPv4PodGateway specifies the IPv4 pod gateway
	IPv4PodGateway string `yaml:"ipv4-pod-gateway,omitempty"`

	// IPv4SvcCIDR specifies the IPv4 service CIDR
	IPv4SvcCIDR string `yaml:"ipv4-svc-cidr,omitempty"`

	// IPv4JoinCIDR specifies the IPv4 join CIDR
	IPv4JoinCIDR string `yaml:"ipv4-join-cidr,omitempty"`

	// Values contains component-specific values keyed by component name
	// Key format: "values-{component-name}"
	Values map[string]string `yaml:",inline"`
}

// ParseConfigMapData parses ConfigMap data into CozystackConfig.
func ParseConfigMapData(data map[string]string) *CozystackConfig {
	cfg := &CozystackConfig{
		Values: make(map[string]string),
	}

	if v, ok := data["bundle-name"]; ok {
		cfg.BundleName = v
	}
	if v, ok := data["bundle-disable"]; ok {
		cfg.BundleDisable = v
	}
	if v, ok := data["bundle-enable"]; ok {
		cfg.BundleEnable = v
	}
	if v, ok := data["oidc-enabled"]; ok {
		cfg.OIDCEnabled = v
	}
	if v, ok := data["root-host"]; ok {
		cfg.RootHost = v
	}
	if v, ok := data["api-server-endpoint"]; ok {
		cfg.APIServerEndpoint = v
	}
	if v, ok := data["cluster-domain"]; ok {
		cfg.ClusterDomain = v
	} else {
		cfg.ClusterDomain = "cozy.local"
	}
	if v, ok := data["ipv4-pod-cidr"]; ok {
		cfg.IPv4PodCIDR = v
	}
	if v, ok := data["ipv4-pod-gateway"]; ok {
		cfg.IPv4PodGateway = v
	}
	if v, ok := data["ipv4-svc-cidr"]; ok {
		cfg.IPv4SvcCIDR = v
	}
	if v, ok := data["ipv4-join-cidr"]; ok {
		cfg.IPv4JoinCIDR = v
	}

	// Extract values-* keys
	for k, v := range data {
		if len(k) > 7 && k[:7] == "values-" {
			cfg.Values[k] = v
		}
	}

	return cfg
}

// GetComponentValues returns values for a specific component.
func (c *CozystackConfig) GetComponentValues(componentName string) (string, bool) {
	key := "values-" + componentName
	v, ok := c.Values[key]
	return v, ok
}

// IsComponentDisabled checks if a component is in the disabled list.
func (c *CozystackConfig) IsComponentDisabled(componentName string) bool {
	if c.BundleDisable == "" {
		return false
	}
	disabled := splitList(c.BundleDisable)
	for _, d := range disabled {
		if d == componentName {
			return true
		}
	}
	return false
}

// IsComponentEnabled checks if an optional component is in the enabled list.
func (c *CozystackConfig) IsComponentEnabled(componentName string) bool {
	if c.BundleEnable == "" {
		return false
	}
	enabled := splitList(c.BundleEnable)
	for _, e := range enabled {
		if e == componentName {
			return true
		}
	}
	return false
}

// IsOIDCEnabled checks if OIDC is enabled.
func (c *CozystackConfig) IsOIDCEnabled() bool {
	return c.OIDCEnabled == "true"
}

// splitList splits a comma-separated string into a slice.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	parts := strings.Split(s, ",")
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
