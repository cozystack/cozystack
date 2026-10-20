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

// Package webhook serves the GPUDevice, VMI and virt-launcher pod admission
// webhooks.
package webhook

import (
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/cozystack/cozystack/internal/gpu"
)

// Paths the chart's webhook configurations call.
const (
	PathValidateDevice = "/gpu/validate-gpudevice"
	PathMutateVMI      = "/gpu/mutate-vmi"
	PathPinPod         = "/gpu/pin-virt-launcher"
)

// Handler serves the GPU admission webhooks.
type Handler struct {
	// Client reads through the manager's cache and writes.
	Client client.Client
	// Reader reads past the cache, where a stale read would let two VMIs
	// take one device or recreate a template device.
	Reader client.Reader
	// Cache is the manager's cache. The manager client reads unstructured
	// objects live, so KubeVirt objects are read here instead.
	Cache  client.Reader
	Config gpu.Config
	// BindTimeout bounds the wait for the binder's answer in one call. The
	// API server calls the VMI webhook again after virt-api's mutator
	// changes the VMI, and both calls share the 60 s request deadline with
	// virt-api's own webhooks.
	BindTimeout  time.Duration
	PollInterval time.Duration
}

// Register adds the GPU admission webhooks to mgr's webhook server.
func Register(mgr manager.Manager, cfg gpu.Config) error {
	h := &Handler{
		Client:       mgr.GetClient(),
		Reader:       mgr.GetAPIReader(),
		Cache:        mgr.GetCache(),
		Config:       cfg,
		BindTimeout:  10 * time.Second,
		PollInterval: 100 * time.Millisecond,
	}
	srv := mgr.GetWebhookServer()
	srv.Register(PathValidateDevice, &admission.Webhook{Handler: admission.HandlerFunc(h.ValidateDevice)})
	srv.Register(PathMutateVMI, &admission.Webhook{Handler: admission.HandlerFunc(h.MutateVMI)})
	srv.Register(PathPinPod, &admission.Webhook{Handler: admission.HandlerFunc(h.PinPod)})
	return nil
}
