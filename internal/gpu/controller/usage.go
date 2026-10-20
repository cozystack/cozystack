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

// workloadMonitorLabel names a Workload's monitor; the Workload controller
// deletes a record whose label is missing or names a monitor that is gone.
const workloadMonitorLabel = "workloads.cozystack.io/monitor"

// recordName is the device's usage record. A device name is itself up to
// 253 characters, so a name too long for the prefix is cut short and
// followed by a hash of the full name, which keeps it stable across passes.
// The hashed form has a prefix of its own, so no device name, which a
// tenant chooses, can reproduce another device's hashed record, and a
// 128-bit hash, so two long names sharing the cut cannot be made to meet.
func recordName(d *gpuv1alpha1.GPUDevice) string {
	const prefix, hashedPrefix = "gpu-", "gpuh-"
	if len(prefix)+len(d.Name) <= validation.DNS1123SubdomainMaxLength {
		return prefix + d.Name
	}
	sum := sha256.Sum256([]byte(d.Name))
	hash := hex.EncodeToString(sum[:16])
	keep := validation.DNS1123SubdomainMaxLength - len(hashedPrefix) - len(hash) - 1
	return hashedPrefix + strings.TrimRight(d.Name[:keep], "-.") + "-" + hash
}

func wantsRecord(d *gpuv1alpha1.GPUDevice) bool {
	return model.InUsageRecords(d.Status.Phase) && !released(d)
}

// usageRecords keeps one Workload per held device, under a WorkloadMonitor
// in each namespace that has devices. The monitor's selector is the
// reserved label admission refuses on every tenant object, so it matches
// nothing and only this controller writes GPU records.
func (r *Reconciler) usageRecords(ctx context.Context, w *world) []error {
	var errs []error
	namespaces := map[string]bool{}
	want := map[types.NamespacedName]bool{}
	for _, d := range w.devices {
		if !released(d) {
			namespaces[d.Namespace] = true
		}
		if wantsRecord(d) {
			want[key(d.Namespace, recordName(d))] = true
		}
	}

	monitors := &cozyv1alpha1.WorkloadMonitorList{}
	if err := r.Client.List(ctx, monitors); err != nil {
		return []error{err}
	}
	for i := range monitors.Items {
		m := &monitors.Items[i]
		if m.Name == gpu.UsageMonitorName && m.Spec.Selector[gpu.LabelUsageMonitor] != "" && !namespaces[m.Namespace] {
			errs = append(errs, client.IgnoreNotFound(r.Client.Delete(ctx, m)))
		}
	}
	for ns := range namespaces {
		errs = append(errs, r.ensureMonitor(ctx, ns))
	}

	for _, d := range w.devices {
		if wantsRecord(d) {
			errs = append(errs, r.ensureRecord(ctx, d))
		}
	}
	records := &cozyv1alpha1.WorkloadList{}
	if err := r.Client.List(ctx, records, client.MatchingLabels{workloadMonitorLabel: gpu.UsageMonitorName}); err != nil {
		return append(errs, err)
	}
	for i := range records.Items {
		rec := &records.Items[i]
		if rec.Status.Kind == gpu.WorkloadKind && !want[key(rec.Namespace, rec.Name)] {
			errs = append(errs, client.IgnoreNotFound(r.Client.Delete(ctx, rec)))
		}
	}
	return errs
}

func (r *Reconciler) ensureMonitor(ctx context.Context, namespace string) error {
	m := &cozyv1alpha1.WorkloadMonitor{ObjectMeta: metav1.ObjectMeta{Name: gpu.UsageMonitorName, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, m, func() error {
		m.Spec.Selector = map[string]string{gpu.LabelUsageMonitor: gpu.LabelValueTrue}
		m.Spec.Kind = gpu.WorkloadKind
		m.Spec.Type = gpu.WorkloadKind
		return nil
	})
	return err
}

func (r *Reconciler) ensureRecord(ctx context.Context, d *gpuv1alpha1.GPUDevice) error {
	rec := &cozyv1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: recordName(d), Namespace: d.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rec, func() error {
		rec.Labels = map[string]string{workloadMonitorLabel: gpu.UsageMonitorName}
		rec.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: gpuv1alpha1.GroupVersion.String(),
			Kind:       "GPUDevice",
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
	return err
}

// deleteRecord removes a released device's record before its finalizer
// goes, so the record never outlives the hold.
func (r *Reconciler) deleteRecord(ctx context.Context, d *gpuv1alpha1.GPUDevice) error {
	rec := &cozyv1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: recordName(d), Namespace: d.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, rec))
}
