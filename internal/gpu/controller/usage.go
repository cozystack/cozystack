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
	"crypto/sha256"
	"encoding/hex"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// recordName is the claim's usage record. A claim name is itself up to
// 253 characters, so a name too long for the prefix is cut short and
// followed by a hash of the full name, which keeps it stable across passes.
// The hashed form has a prefix of its own, so no claim name, which a
// tenant chooses, can reproduce another claim's hashed record, and a
// 128-bit hash, so two long names sharing the cut cannot be made to meet.
func recordName(d *gpuv1alpha1.GPUClaim) string {
	const prefix, hashedPrefix = "gpu-", "gpuh-"
	if len(prefix)+len(d.Name) <= validation.DNS1123SubdomainMaxLength {
		return prefix + d.Name
	}
	sum := sha256.Sum256([]byte(d.Name))
	hash := hex.EncodeToString(sum[:16])
	keep := validation.DNS1123SubdomainMaxLength - len(hashedPrefix) - len(hash) - 1
	return hashedPrefix + strings.TrimRight(d.Name[:keep], "-.") + "-" + hash
}

func wantsRecord(d *gpuv1alpha1.GPUClaim) bool {
	return model.InUsageRecords(d.Status.Phase) && !released(d)
}

// usageRecords keeps one Workload per claim that holds GPUs, owned by its
// GPUClaim and under no WorkloadMonitor, so no tenant label can add a record
// or drop one. The Workload controller leaves records owned by a GPUClaim
// alone, and garbage collection removes a record with its claim.
func (r *Reconciler) usageRecords(ctx context.Context, w *world) []error {
	var errs []error
	want := map[types.NamespacedName]bool{}
	for _, d := range w.claims {
		if wantsRecord(d) {
			want[key(d.Namespace, recordName(d))] = true
			errs = append(errs, r.ensureRecord(ctx, d))
		}
	}
	records := &cozyv1alpha1.WorkloadList{}
	if err := r.Client.List(ctx, records); err != nil {
		return append(errs, err)
	}
	for i := range records.Items {
		rec := &records.Items[i]
		if ownedByClaim(rec) && !want[key(rec.Namespace, rec.Name)] {
			errs = append(errs, client.IgnoreNotFound(r.Client.Delete(ctx, rec)))
		}
	}
	return errs
}

func ownedByClaim(o client.Object) bool {
	for _, ref := range o.GetOwnerReferences() {
		if ref.APIVersion == gpuv1alpha1.GroupVersion.String() && ref.Kind == claimKind {
			return true
		}
	}
	return false
}

const claimKind = "GPUClaim"

func (r *Reconciler) ensureRecord(ctx context.Context, d *gpuv1alpha1.GPUClaim) error {
	rec := &cozyv1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: recordName(d), Namespace: d.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rec, func() error {
		rec.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: gpuv1alpha1.GroupVersion.String(),
			Kind:       claimKind,
			Name:       d.Name,
			UID:        d.UID,
		}}
		rec.Status = cozyv1alpha1.WorkloadStatus{
			Kind:        gpu.WorkloadKind,
			Type:        d.Spec.ClassName,
			Resources:   map[string]resource.Quantity{string(model.ClassQuotaKey(d.Spec.ClassName)): *resource.NewQuantity(int64(d.Spec.Count), resource.DecimalSI)},
			Operational: true,
		}
		return nil
	})
	if apierrors.IsAlreadyExists(err) {
		// A stale cache read the record as missing; the next pass updates it.
		return nil
	}
	return err
}

// deleteRecord removes a released claim's record before its finalizer
// goes, so the record never outlives the hold.
func (r *Reconciler) deleteRecord(ctx context.Context, d *gpuv1alpha1.GPUClaim) error {
	rec := &cozyv1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: recordName(d), Namespace: d.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, rec))
}
