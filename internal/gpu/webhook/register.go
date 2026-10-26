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

// Package webhook serves the GPUClaim, VMI and virt-launcher pod admission
// webhooks.
package webhook

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/cozystack/cozystack/internal/gpu"
)

// Paths the chart's webhook configurations call.
const (
	PathValidateClaim = "/gpu/validate-gpuclaim"
	PathMutateVMI     = "/gpu/mutate-vmi"
	PathGatePod       = "/gpu/gate-virt-launcher"
)

// Handler serves the GPU admission webhooks.
type Handler struct {
	// Client writes.
	Client client.Client
	// Reader reads past the cache. Every replica serves the webhooks while
	// only the leader binds, so a replica whose watch lags would refuse or
	// admit by a cluster that no longer exists: a claim another VMI has
	// just taken, a budget just spent, a table entry just removed.
	Reader client.Reader
	Config gpu.Config
}

// Register adds the GPU admission webhooks to mgr's webhook server.
func Register(mgr manager.Manager, cfg gpu.Config) error {
	h := &Handler{
		Client: mgr.GetClient(),
		Reader: mgr.GetAPIReader(),
		Config: cfg,
	}
	srv := mgr.GetWebhookServer()
	srv.Register(PathValidateClaim, &admission.Webhook{Handler: admission.HandlerFunc(h.ValidateClaim)})
	srv.Register(PathMutateVMI, &admission.Webhook{Handler: admission.HandlerFunc(h.MutateVMI)})
	srv.Register(PathGatePod, &admission.Webhook{Handler: admission.HandlerFunc(h.GatePod)})
	return nil
}
