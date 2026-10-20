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

package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

func (r *Reconciler) ensureFinalizers(ctx context.Context, w *world) []error {
	var errs []error
	for _, c := range w.classes {
		if c.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(c, gpu.FinalizerClass) {
			errs = append(errs, r.Client.Update(ctx, c))
		}
	}
	for _, d := range w.claims {
		if d.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(d, gpu.FinalizerClaim) {
			errs = append(errs, r.Client.Update(ctx, d))
		}
	}
	return errs
}

func vmiOwner(d *gpuv1alpha1.GPUClaim) types.UID {
	for _, ref := range d.OwnerReferences {
		if ref.Kind == vmiGVK.Kind && ref.APIVersion == vmiGVK.GroupVersion().String() {
			return ref.UID
		}
	}
	return ""
}

// attachedVMI returns the VMI a claim's attachment names, or nil when it
// does not exist. A template claim owned by an earlier VMI of the same
// name does not belong to the current one.
func (w *world) attachedVMI(d *gpuv1alpha1.GPUClaim) *vmi {
	if d.Status.AttachedTo == nil {
		return nil
	}
	v := w.vmis[key(d.Namespace, d.Status.AttachedTo.VMI)]
	if v == nil {
		return nil
	}
	if owner := vmiOwner(d); owner != "" && owner != v.UID {
		return nil
	}
	return v
}

// attachments moves claims into and out of Attached as their VMIs come
// and go, deletes template claims whose VMI is final or gone, and clears
// attachments whose VMI never appeared.
func (r *Reconciler) attachments(ctx context.Context, w *world) []error {
	var errs []error
	seen := map[string]bool{}
	for _, d := range w.claims {
		if d.Status.AttachedTo == nil {
			// Created but not attached yet: gates attaches it while its VMI
			// lives.
			if isTemplate(d) && d.DeletionTimestamp.IsZero() && !w.ownerLives(d) {
				errs = append(errs, r.deleteClaim(ctx, d))
			}
			continue
		}
		v := w.attachedVMI(d)
		phase := d.Status.Phase
		switch {
		case v != nil && !v.final():
			errs = append(errs, r.attachLive(ctx, w, d))
		case v != nil, phase == gpuv1alpha1.GPUClaimAttached, phase == gpuv1alpha1.GPUClaimLost, vmiOwner(d) != "":
			errs = append(errs, r.detach(ctx, w, d))
		default:
			k := d.Namespace + "/" + d.Name + "/" + d.Status.AttachedTo.VMI
			seen[k] = true
			if w.now.Sub(r.attachedAt(w, k)) >= r.Config.AttachmentGrace {
				errs = append(errs, r.detach(ctx, w, d))
			}
		}
	}
	r.mu.Lock()
	for k := range r.attachedSince {
		if !seen[k] {
			delete(r.attachedSince, k)
		}
	}
	r.mu.Unlock()
	return errs
}

// attachedAt is when this controller first saw an attachment whose VMI has
// not appeared.
func (r *Reconciler) attachedAt(w *world, k string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	since, ok := r.attachedSince[k]
	if !ok {
		since = w.now
		r.attachedSince[k] = since
	}
	return since
}

func (r *Reconciler) attachLive(ctx context.Context, w *world, d *gpuv1alpha1.GPUClaim) error {
	switch d.Status.Phase {
	case gpuv1alpha1.GPUClaimBound:
	case gpuv1alpha1.GPUClaimLost:
		// The node came back while the VMI still runs.
		if n := w.nodes[d.Status.NodeName]; n == nil || !model.Ready(n) || w.lost[n.Name].covers(w.resourceName(d.Spec.ClassName)) {
			return nil
		}
	default:
		return nil
	}
	d.Status.Phase = gpuv1alpha1.GPUClaimAttached
	return r.updateStatus(ctx, w, d)
}

// detach ends an attachment whose VMI is final, removed, or never appeared.
// A template claim lives only with its VMI, so it is deleted instead.
func (r *Reconciler) detach(ctx context.Context, w *world, d *gpuv1alpha1.GPUClaim) error {
	if isTemplate(d) {
		if !d.DeletionTimestamp.IsZero() {
			return nil
		}
		return r.deleteClaim(ctx, d)
	}
	d.Status.AttachedTo = nil
	if d.Status.Phase == gpuv1alpha1.GPUClaimAttached {
		d.Status.Phase = gpuv1alpha1.GPUClaimBound
	}
	return r.updateStatus(ctx, w, d)
}

func (r *Reconciler) deleteClaim(ctx context.Context, d *gpuv1alpha1.GPUClaim) error {
	log.FromContext(ctx).Info("deleting template GPUClaim", "namespace", d.Namespace, "name", d.Name)
	return client.IgnoreNotFound(r.Client.Delete(ctx, d))
}

// usedByVMI reports whether a VMI that can still hold the claim's GPUs
// uses it. With the feature disabled no VMI cache exists, so the VMI is
// read directly; a cluster without KubeVirt has none.
func (r *Reconciler) usedByVMI(ctx context.Context, w *world, d *gpuv1alpha1.GPUClaim) (bool, error) {
	if d.Status.AttachedTo == nil {
		return false, nil
	}
	if r.Config.Enabled {
		v := w.attachedVMI(d)
		if v == nil && d.Status.Phase != gpuv1alpha1.GPUClaimAttached && d.Status.Phase != gpuv1alpha1.GPUClaimLost &&
			vmiOwner(d) == "" {
			// A fresh attachment whose VMI the cache has not shown yet, as
			// attachments() treats it: the VMI may be starting with the GPU.
			k := d.Namespace + "/" + d.Name + "/" + d.Status.AttachedTo.VMI
			return w.now.Sub(r.attachedAt(w, k)) < r.Config.AttachmentGrace, nil
		}
		return v != nil && !v.final(), nil
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(vmiGVK)
	err := r.Reader.Get(ctx, key(d.Namespace, d.Status.AttachedTo.VMI), u)
	switch {
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err), runtime.IsNotRegisteredError(err):
		return false, nil
	case err != nil:
		return false, err
	}
	v := vmiFrom(u)
	if owner := vmiOwner(d); owner != "" && owner != v.UID {
		return false, nil
	}
	return !v.final(), nil
}

// releaseDeleted lets a deleted claim go once no VMI uses it, and a
// deleted class once no claim of it holds or owes GPUs.
func (r *Reconciler) releaseDeleted(ctx context.Context, w *world) []error {
	var errs []error
	for _, d := range w.claims {
		if d.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(d, gpu.FinalizerClaim) {
			continue
		}
		used, err := r.usedByVMI(ctx, w, d)
		if err != nil || used {
			errs = append(errs, err)
			continue
		}
		if err := r.deleteRecord(ctx, d); err != nil {
			errs = append(errs, err)
			continue
		}
		controllerutil.RemoveFinalizer(d, gpu.FinalizerClaim)
		if err := client.IgnoreNotFound(r.Client.Update(ctx, d)); err != nil {
			// The claim stays, so its class must too.
			controllerutil.AddFinalizer(d, gpu.FinalizerClaim)
			errs = append(errs, err)
		}
	}
	for _, c := range w.classes {
		if c.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(c, gpu.FinalizerClass) {
			continue
		}
		held := false
		for _, d := range w.claims {
			if d.Spec.ClassName == c.Name && model.CountsAgainstBudget(d.Status.Phase) && !released(d) {
				held = true
				break
			}
		}
		if held {
			continue
		}
		controllerutil.RemoveFinalizer(c, gpu.FinalizerClass)
		errs = append(errs, client.IgnoreNotFound(r.Client.Update(ctx, c)))
	}
	return errs
}

// released reports a claim whose finalizer this pass has removed.
func released(d *gpuv1alpha1.GPUClaim) bool {
	return !d.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(d, gpu.FinalizerClaim)
}

// ownerLives reports whether the VMI that owns a template claim exists and
// is not final.
func (w *world) ownerLives(d *gpuv1alpha1.GPUClaim) bool {
	uid := vmiOwner(d)
	for _, ref := range d.OwnerReferences {
		if ref.UID != uid || uid == "" {
			continue
		}
		v := w.vmis[key(d.Namespace, ref.Name)]
		return v != nil && v.UID == uid && !v.final()
	}
	return false
}

// deleteVMI deletes the unscheduled VMI of a claim that cannot stay on its
// node once the VMI's pod is pinned, so the claim becomes unattached and
// can move. What the VM does next follows its runStrategy.
func (r *Reconciler) deleteVMI(ctx context.Context, v *vmi, why string) error {
	log.FromContext(ctx).Info("deleting unscheduled VMI", "namespace", v.Namespace, "name", v.Name, "reason", why)
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(vmiGVK)
	u.SetNamespace(v.Namespace)
	u.SetName(v.Name)
	// The VMI is read from the cache; the version precondition keeps one
	// scheduled since then from being deleted on that stale view.
	err := r.Client.Delete(ctx, u, client.Preconditions{UID: &v.UID, ResourceVersion: &v.ResourceVersion})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete VMI %s/%s: %w", v.Namespace, v.Name, err)
	}
	return nil
}
