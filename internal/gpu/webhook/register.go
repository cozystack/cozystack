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

// Package webhook serves the VMI and virt-launcher pod admission webhooks
// that turn class entries into KubeVirt GPU entries.
package webhook

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

const (
	PathMutateVMI      = "/gpu/mutate-vmi"
	PathMutateLauncher = "/gpu/mutate-virt-launcher"
)

var (
	vmiGVK      = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}
	kubeVirtGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "KubeVirt"}
)

type Handler struct {
	// Reader reads past the cache: every replica serves the webhooks, and
	// one whose watch lags would admit by classes, a table or an
	// entitlement that no longer hold.
	Reader client.Reader
	// ResourceNames is the hoster's gpu.resourceNames.
	ResourceNames []string
}

func Register(mgr manager.Manager, resourceNames []string) {
	h := &Handler{Reader: mgr.GetAPIReader(), ResourceNames: resourceNames}
	srv := mgr.GetWebhookServer()
	srv.Register(PathMutateVMI, &admission.Webhook{Handler: admission.HandlerFunc(h.MutateVMI)})
	srv.Register(PathMutateLauncher, &admission.Webhook{Handler: admission.HandlerFunc(h.MutateLauncher)})
}

func (h *Handler) classes(ctx context.Context) (map[string]*cozyv1alpha1.GPUClass, error) {
	l := &cozyv1alpha1.GPUClassList{}
	if err := h.Reader.List(ctx, l); err != nil {
		return nil, fmt.Errorf("list GPUClasses: %w", err)
	}
	out := make(map[string]*cozyv1alpha1.GPUClass, len(l.Items))
	for i := range l.Items {
		out[l.Items[i].Name] = &l.Items[i]
	}
	return out, nil
}

func (h *Handler) entitled(ctx context.Context, ns string) (bool, error) {
	n := &corev1.Namespace{}
	err := h.Reader.Get(ctx, client.ObjectKey{Name: ns}, n)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get namespace %s: %w", ns, err)
	}
	return model.Entitled(n.Labels), nil
}

// KubeVirt may not be installed yet; absence leaves its optional allowlist unset.
func (h *Handler) kubeVirt(ctx context.Context) (*unstructured.Unstructured, error) {
	kv := &unstructured.Unstructured{}
	kv.SetGroupVersionKind(kubeVirtGVK)
	err := h.Reader.Get(ctx, client.ObjectKey{Namespace: model.KubeVirtNamespace, Name: model.KubeVirtName}, kv)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get KubeVirt %s/%s: %w", model.KubeVirtNamespace, model.KubeVirtName, err)
	}
	return kv, nil
}
