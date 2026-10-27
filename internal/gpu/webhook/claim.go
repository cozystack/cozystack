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
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// ValidateClaim refuses a tenant's order that cannot be met now: an
// unknown or deleted class, a count above maxCount, the class's free units
// or one node's, a budget of the tenant or an ancestor, or a vGPU class
// without the entitlement. The binder checks again before it binds, since
// two orders admitted at once can exceed a limit together.
func (h *Handler) ValidateClaim(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	d := &gpuv1alpha1.GPUClaim{}
	if err := json.Unmarshal(req.Object.Raw, d); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	// A ValidatingAdmissionPolicy keeps tenants from setting the mark, so a
	// marked claim comes from the controller, which creates it from a
	// claimTemplate entry the VMI webhook has checked and binds it under
	// the budget and capacity checks.
	if hasTemplateMark(d) {
		return admission.Allowed("")
	}

	c := &gpuv1alpha1.GPUClass{}
	err := h.Reader.Get(ctx, client.ObjectKey{Name: d.Spec.ClassName}, c)
	switch {
	case apierrors.IsNotFound(err):
		return admission.Denied(fmt.Sprintf("GPUClass %s does not exist", d.Spec.ClassName))
	case err != nil:
		return admission.Errored(http.StatusInternalServerError, err)
	case !c.DeletionTimestamp.IsZero():
		return admission.Denied(fmt.Sprintf("GPUClass %s is being deleted and takes no new orders", c.Name))
	}

	count := d.Spec.Count
	if limit, ok := model.EffectiveMaxCount(&c.Spec); ok && count > limit {
		return admission.Denied(fmt.Sprintf("cannot order %d of GPUClass %s: a VM takes at most maxCount %d", count, c.Name, limit))
	}

	classes, err := h.classes(ctx)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	snap, err := h.budgetSnapshot(ctx, d.Namespace, classes)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	entitled, err := h.entitled(ctx, d.Namespace)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if err := snap.CheckOrder(d.Namespace, c.Name, int64(count), entitled); err != nil {
		return admission.Denied(err.Error())
	}

	// Free capacity is the binder's, published on the class. Until it has
	// published any, the binder alone decides.
	if free := c.Status.Free; free != nil && count > *free {
		return admission.Denied(fmt.Sprintf("cannot order %d of GPUClass %s: the class has %d free units", count, c.Name, *free))
	}
	if perNode := c.Status.MaxFreePerNode; perNode != nil && count > *perNode {
		return admission.Denied(fmt.Sprintf("cannot order %d of GPUClass %s: no node has more than %d free units", count, c.Name, *perNode))
	}
	return admission.Allowed("")
}
