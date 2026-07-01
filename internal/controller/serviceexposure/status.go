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

package serviceexposure

import (
	"context"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

// reconcileError carries a machine-readable Reason for the Ready=False
// condition alongside the underlying error, so operators reading
// `kubectl get svcexp` see e.g. ClassNotFound rather than a generic
// failure.
type reconcileError struct {
	reason string
	err    error
}

func (e *reconcileError) Error() string { return e.err.Error() }
func (e *reconcileError) Unwrap() error { return e.err }

// failf builds a reconcileError with the given reason.
func failf(reason string, err error) error {
	return &reconcileError{reason: reason, err: err}
}

// reasonOf extracts the Reason from a reconcileError, defaulting to
// "ReconcileError" for plain errors.
func reasonOf(err error) string {
	if re, ok := err.(*reconcileError); ok {
		return re.reason
	}
	return "ReconcileError"
}

// markReady writes a successful status: resolved backend, assigned IPs and
// a Ready condition reflecting whether the backend has actually allocated.
func (r *Reconciler) markReady(
	ctx context.Context,
	exp *networkv1alpha1.ServiceExposure,
	backend string,
	assignedIPs []string,
	ready bool,
	notReadyReason string,
) error {
	cond := metav1.Condition{
		Type:               "Ready",
		ObservedGeneration: exp.Generation,
	}
	if ready {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Reconciled"
		cond.Message = "Exposure programmed by backend " + backend
	} else {
		cond.Status = metav1.ConditionFalse
		cond.Reason = notReadyReason
		cond.Message = "Backend " + backend + " has not assigned an address yet"
	}

	stale := exp.DeepCopy()
	stale.Status.ObservedGeneration = exp.Generation
	stale.Status.ResolvedBackend = backend
	stale.Status.AssignedIPs = assignedIPs
	apimeta.SetStatusCondition(&stale.Status.Conditions, cond)

	if statusEqual(exp.Status, stale.Status) {
		return nil
	}
	exp.Status = stale.Status
	return r.Status().Update(ctx, exp)
}

// markFailed records a Ready=False condition with the cause's reason.
func (r *Reconciler) markFailed(ctx context.Context, exp *networkv1alpha1.ServiceExposure, cause error) error {
	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: exp.Generation,
		Reason:             reasonOf(cause),
		Message:            cause.Error(),
	}
	stale := exp.DeepCopy()
	stale.Status.ObservedGeneration = exp.Generation
	apimeta.SetStatusCondition(&stale.Status.Conditions, cond)
	if statusEqual(exp.Status, stale.Status) {
		return nil
	}
	exp.Status = stale.Status
	return r.Status().Update(ctx, exp)
}

func statusEqual(a, b networkv1alpha1.ServiceExposureStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration {
		return false
	}
	if a.ResolvedBackend != b.ResolvedBackend {
		return false
	}
	if !stringSliceEqual(a.AssignedIPs, b.AssignedIPs) {
		return false
	}
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		ac, bc := a.Conditions[i], b.Conditions[i]
		if ac.Type != bc.Type ||
			ac.Status != bc.Status ||
			ac.Reason != bc.Reason ||
			ac.Message != bc.Message ||
			ac.ObservedGeneration != bc.ObservedGeneration {
			return false
		}
	}
	return true
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
