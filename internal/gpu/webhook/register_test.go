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

package webhook

import (
	"net/http/httptest"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/cozystack/cozystack/internal/gpu"
)

// fakeManager provides what Register uses; any other method panics.
type fakeManager struct {
	manager.Manager
	server webhook.Server
	c      client.Client
}

func (m *fakeManager) GetWebhookServer() webhook.Server { return m.server }
func (m *fakeManager) GetClient() client.Client         { return m.c }
func (m *fakeManager) GetAPIReader() client.Reader      { return m.c }
func (m *fakeManager) GetCache() cache.Cache            { return nil }

func TestRegisterServesEveryPath(t *testing.T) {
	m := &fakeManager{server: webhook.NewServer(webhook.Options{}), c: newClient(t)}
	if err := Register(m, gpu.Config{Enabled: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, path := range []string{PathValidateDevice, PathMutateVMI, PathPinPod} {
		if _, pattern := m.server.WebhookMux().Handler(httptest.NewRequest("POST", path, nil)); pattern != path {
			t.Errorf("%s is not served (matched %q)", path, pattern)
		}
	}
}
