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

package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/cozystack/cozystack/pkg/config"
)

// The server runs without etcd: every resource is backed by the Kubernetes
// API. Validate and Config must accept the recommended options with Etcd left
// nil, and the result must build the generic server.
func TestCozyServerOptions_ConfigBuildsServerWithoutEtcd(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	// Nothing listens on this address. The authentication config lookup does
	// dial it, gets refused, and TolerateInClusterLookupFailure lets it go on.
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: u
  user: {token: t}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)

	o := NewCozyServerOptions(nil, nil)
	if o.RecommendedOptions.Etcd != nil {
		t.Fatal("NewCozyServerOptions must leave Etcd nil")
	}
	o.ResourceConfig = &config.ResourceConfig{Resources: []config.Resource{{
		Application: config.ApplicationConfig{Kind: "Redis", Singular: "redis", Plural: "redises"},
		Release:     config.ReleaseConfig{Prefix: "redis-"},
	}}}
	ro := o.RecommendedOptions
	ro.CoreAPI.CoreAPIKubeconfigPath = kubeconfig
	ro.Authentication.RemoteKubeConfigFile = kubeconfig
	ro.Authentication.TolerateInClusterLookupFailure = true
	ro.Authorization.RemoteKubeConfigFile = kubeconfig
	ro.SecureServing.ServerCert.CertDirectory = dir
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ro.SecureServing.Listener = ln

	if err := o.Validate(nil); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	cfg, err := o.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.GenericConfig.RESTOptionsGetter != nil {
		t.Errorf("RESTOptionsGetter = %T, want none without etcd", cfg.GenericConfig.RESTOptionsGetter)
	}
	// The cozystack server itself needs a live cluster to build its storage;
	// the generic server it embeds does not.
	if _, err := cfg.GenericConfig.Complete().New("cozystack-api-test", genericapiserver.NewEmptyDelegate()); err != nil {
		t.Fatalf("New: %v", err)
	}
}
